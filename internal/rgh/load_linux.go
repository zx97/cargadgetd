//go:build linux && cgo

package rgh

/*
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdint.h>
#include <stdlib.h>

static void *rgh_dlopen(const char *path, char **err)
{
	void *h;

	dlerror();
	h = dlopen(path, RTLD_NOW | RTLD_GLOBAL);
	if (!h && err)
		*err = (char *)dlerror();
	return h;
}

static void *rgh_dlsym(void *h, const char *name, char **err)
{
	void *s;

	dlerror();
	s = dlsym(h, name);
	if (!s && err) {
		char *e = (char *)dlerror();
		*err = e ? e : "missing symbol";
	}
	return s;
}

typedef uint8_t *(*rgh_pem_fn)(size_t *);
typedef char *(*rgh_probe_fn)(void);
typedef long long (*rgh_exp_fn)(void);

static uint8_t *rgh_pem(void *h, const char *name, size_t *n, char **err)
{
	rgh_pem_fn fn = (rgh_pem_fn)rgh_dlsym(h, name, err);
	if (!fn)
		return 0;
	if (err)
		*err = 0;
	return fn(n);
}

static char *rgh_probe(void *h, char **err)
{
	rgh_probe_fn fn = (rgh_probe_fn)rgh_dlsym(h, "reversegearhead_probe", err);
	if (!fn)
		return 0;
	if (err)
		*err = 0;
	return fn();
}

static long long rgh_expires(void *h, char **err)
{
	rgh_exp_fn fn = (rgh_exp_fn)rgh_dlsym(h, "reversegearhead_expires_in", err);
	if (!fn)
		return 0;
	if (err)
		*err = 0;
	return fn();
}
*/
import "C"

import (
	"fmt"
	"os"
	"unsafe"
)

// Open dlopens reversegearhead.so and copies process-lifetime PEM pointers.
// The handle is never closed (Go c-shared runtime stays loaded; the shell
// later dlopens the same path as an app).
func Open(path string) (*Material, error) {
	// An extracted Android Auto pair is a directory of PEMs, not a plugin: the
	// same function serves both so callers do not care which source they have.
	if isDir(path) && looksLikePair(path) {
		return openDir(path)
	}
	path = ResolvePath(path)
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, missingErr(path)
		}
		return nil, &LoadError{Path: path, Err: err}
	}
	if st.IsDir() {
		return nil, &LoadError{Path: path, Err: fmt.Errorf("need reversegearhead.so, got a directory")}
	}

	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	var cerr *C.char
	h := C.rgh_dlopen(cpath, &cerr)
	if h == nil {
		msg := "dlopen failed"
		if cerr != nil {
			msg = C.GoString(cerr)
		}
		return nil, &LoadError{Path: path, Err: fmt.Errorf("%s", msg)}
	}

	cert, err := pem(h, "reversegearhead_android_auto_crt")
	if err != nil {
		return nil, &LoadError{Path: path, Err: err}
	}
	key, err := pem(h, "reversegearhead_android_auto_key")
	if err != nil {
		return nil, &LoadError{Path: path, Err: err}
	}
	if len(cert) == 0 || len(key) == 0 {
		return nil, &LoadError{Path: path, Err: fmt.Errorf("empty android_auto.crt or android_auto.key")}
	}

	m := &Material{Path: path, Cert: cert, Key: key}
	if ca, err := pem(h, "reversegearhead_ca_crt"); err == nil {
		m.CA = ca
	}

	cerr = nil
	if p := C.rgh_probe(h, &cerr); p != nil {
		m.Probe = C.GoString(p)
	} else if cerr != nil {
		return nil, &LoadError{Path: path, Err: fmt.Errorf("reversegearhead_probe: %s", C.GoString(cerr))}
	}

	cerr = nil
	m.ExpiresIn = int64(C.rgh_expires(h, &cerr))
	if cerr != nil {
		return nil, &LoadError{Path: path, Err: fmt.Errorf("reversegearhead_expires_in: %s", C.GoString(cerr))}
	}
	return m, nil
}

func pem(h unsafe.Pointer, name string) ([]byte, error) {
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	var n C.size_t
	var cerr *C.char
	p := C.rgh_pem(h, cname, &n, &cerr)
	if p == nil {
		msg := "nil"
		if cerr != nil {
			msg = C.GoString(cerr)
		}
		return nil, fmt.Errorf("%s: %s", name, msg)
	}
	if n == 0 {
		return nil, fmt.Errorf("%s: empty", name)
	}
	return C.GoBytes(unsafe.Pointer(p), C.int(n)), nil
}
