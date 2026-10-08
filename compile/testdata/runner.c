/* Test-only Lua 5.5.1 bridge. This is NOT a Lua binary chunk loader.
 * stdin: mode byte ('s' or 'p'), then source string or recursive prototype.
 * u32/u64 are little endian; strings are u32 byte length + raw bytes.
 * Proto: params, isvararg, stack, varargtable (u8 each), code count + u32s,
 * constants count + (tag u8, bits u64, string), upvalues count +
 * (instack, index, kind: u8, name string), children count + prototypes.
 * Only trusted compiler test output may be executed: this is not a verifier.
 */
#include <assert.h>
#include <inttypes.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "lua.h"
#include "lauxlib.h"
#include "lualib.h"
#include "lfunc.h"
#include "lmem.h"
#include "lobject.h"
#include "lstate.h"
#include "lstring.h"

#if LUA_VERSION_RELEASE_NUM != 50501
#error "runner requires the official Lua 5.5.1 sources"
#endif
_Static_assert(sizeof(lua_Integer) == 8, "requires 64-bit Lua integers");
_Static_assert(sizeof(lua_Number) == 8, "requires binary64 Lua numbers");
_Static_assert(sizeof(Instruction) == 4, "requires 32-bit instructions");

static void badwire(const char *message) {
  fprintf(stderr, "runner protocol: %s\n", message);
  exit(2);
}

static unsigned byte(void) {
  int c = getchar();
  if (c == EOF) badwire("unexpected EOF");
  return (unsigned)c;
}

static uint64_t little(unsigned n) {
  uint64_t v = 0;
  for (unsigned i = 0; i < n; i++) v |= (uint64_t)byte() << (8 * i);
  return v;
}

static int count(void) {
  uint64_t n = little(4);
  /* Deliberate test-protocol limit, not a Lua language limit. */
  if (n > 16 * 1024 * 1024) badwire("count exceeds test limit");
  return (int)n;
}

static char *string(size_t *len) {
  *len = (size_t)count();
  char *s = malloc(*len + 1);
  if (!s) badwire("out of memory");
  if (fread(s, 1, *len, stdin) != *len) badwire("truncated string");
  s[*len] = '\0';
  return s;
}

static TString *luastring(lua_State *L) {
  size_t n;
  char *s = string(&n);
  TString *ts = luaS_newlstr(L, s, n);
  free(s);
  return ts;
}

static Proto *prototype(lua_State *L, unsigned depth) {
  if (depth > 200) badwire("prototype nesting exceeds test limit");
  Proto *p = luaF_newproto(L);
  p->source = luaS_newliteral(L, "=runtime");
  p->numparams = byte();
  unsigned vararg = byte();
  p->maxstacksize = byte();
  unsigned table = byte();
  if (vararg > 1 || table > 1 || (table && !vararg) ||
      p->maxstacksize < 2 || p->numparams > p->maxstacksize)
    badwire("invalid function metadata");
  p->flag = table ? PF_VATAB : vararg ? PF_VAHID : 0;

  p->sizecode = count();
  p->code = luaM_newvectorchecked(L, p->sizecode, Instruction);
  for (int i = 0; i < p->sizecode; i++) p->code[i] = (Instruction)little(4);

  p->sizek = count();
  p->k = luaM_newvectorchecked(L, p->sizek, TValue);
  for (int i = 0; i < p->sizek; i++) {
    unsigned tag = byte();
    uint64_t bits = little(8);
    size_t n;
    char *s = string(&n);
    TValue *v = &p->k[i];
    switch (tag) {
      case 0: setnilvalue(v); break;
      case 1:
        if (bits > 1) badwire("invalid boolean");
        if (bits) { setbtvalue(v); } else { setbfvalue(v); }
        break;
      case 2: {
        lua_Integer value;
        memcpy(&value, &bits, sizeof(value));
        setivalue(v, value);
        break;
      }
      case 3: {
        lua_Number value;
        memcpy(&value, &bits, sizeof(value));
        setfltvalue(v, value);
        break;
      }
      case 4: {
        TString *ts = luaS_newlstr(L, s, n);
        setsvalue(L, v, ts);
        break;
      }
      default: badwire("unknown constant tag");
    }
    free(s);
  }

  p->sizeupvalues = count();
  if (p->sizeupvalues > 255) badwire("too many upvalues");
  p->upvalues = luaM_newvectorchecked(L, p->sizeupvalues, Upvaldesc);
  for (int i = 0; i < p->sizeupvalues; i++) {
    Upvaldesc *u = &p->upvalues[i];
    u->instack = byte();
    u->idx = byte();
    u->kind = byte();
    u->name = luastring(L);
    if (u->instack > 1 || u->kind > 6) badwire("invalid upvalue");
  }

  p->sizep = count();
  p->p = luaM_newvectorchecked(L, p->sizep, Proto *);
  for (int i = 0; i < p->sizep; i++) p->p[i] = prototype(L, depth + 1);
  return p;
}

static void import(lua_State *L) {
  lua_gc(L, LUA_GCSTOP);
  /* LUA_GCSTOP alone does not stop emergency GC over half-filled arrays. */
  G(L)->gcstopem = 1;
  Proto *p = prototype(L, 0);
  if (p->sizeupvalues != 1 || strcmp(getstr(p->upvalues[0].name), "_ENV"))
    badwire("root must have exactly one _ENV upvalue");
  LClosure *cl = luaF_newLclosure(L, 1);
  cl->p = p;
  luaF_initupvals(L, cl);
  /* Reserve/anchor via the API instead of manually advancing L->top. */
  lua_pushnil(L);
  setclLvalue2s(L, L->top.p - 1, cl);
  lua_pushglobaltable(L);
  setobj(L, cl->upvals[0]->v.p, s2v(L->top.p - 1));
  lua_pop(L, 1);
  G(L)->gcstopem = 0;
  lua_gc(L, LUA_GCRESTART);
  /* Exercise rooting before executing any imported instruction. */
  lua_gc(L, LUA_GCCOLLECT);
}

static void results(lua_State *L) {
  printf("\n@results %d\n", lua_gettop(L));
  for (int i = 1; i <= lua_gettop(L); i++) {
    switch (lua_type(L, i)) {
      case LUA_TNIL: puts("nil"); break;
      case LUA_TBOOLEAN: printf("bool %d\n", lua_toboolean(L, i)); break;
      case LUA_TNUMBER:
        if (lua_isinteger(L, i))
          printf("int %" PRId64 "\n", (int64_t)lua_tointeger(L, i));
        else
          printf("float %a\n", (double)lua_tonumber(L, i));
        break;
      case LUA_TSTRING: {
        size_t n;
        const unsigned char *s = (const unsigned char *)lua_tolstring(L, i, &n);
        printf("string %zu ", n);
        for (size_t j = 0; j < n; j++) printf("%02x", s[j]);
        putchar('\n');
        break;
      }
      default: badwire("tests must return only primitive values");
    }
  }
}

int main(void) {
  lua_State *L = luaL_newstate();
  if (!L) badwire("cannot create Lua state");
  luaL_openlibs(L);
  unsigned mode = byte();
  int status;
  if (mode == 's') {
    size_t n;
    char *s = string(&n);
    status = luaL_loadbufferx(L, s, n, "=runtime", "t");
    free(s);
    if (status != LUA_OK) {
      fprintf(stderr, "source load: %s\n", lua_tostring(L, -1));
      lua_close(L);
      return 2;
    }
  } else if (mode == 'p') {
    import(L);
  } else {
    badwire("unknown mode");
  }
  if (getchar() != EOF) badwire("trailing input");
  status = lua_pcall(L, 0, LUA_MULTRET, 0);
  printf("\n@status %d\n", status);
  if (status == LUA_OK) {
    results(L);
  } else {
    /* No line tables in the bridge: compare status, not diagnostic locations. */
    const char *error = lua_tostring(L, -1);
    fprintf(stderr, "runtime: %s\n", error ? error : "non-string error");
  }
  lua_close(L);
  return status == LUA_OK ? 0 : 1;
}
