//go:build linux || darwin

// Command faulthost loads the plugin the way CLIProxyAPI does and keeps
// recovering nil-pointer faults in host goroutines while it calls the plugin.
// Both are Go runtimes in one process, so a plugin that takes over the host's
// fault handling turns those recoverable panics into a crash.
//
//	faulthost [-for 15s] path/to/claude-keep-rolling.{so,dylib}
package main

/*
#cgo linux LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdint.h>
#include <stdlib.h>

typedef struct { void* ptr; size_t len; } cliproxy_buffer;
typedef int (*host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*host_free_fn)(void*, size_t);
typedef struct { uint32_t abi_version; void* host_ctx; host_call_fn call; host_free_fn free_buffer; } host_api;
typedef int (*plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*plugin_free_fn)(void*, size_t);
typedef struct { uint32_t abi_version; plugin_call_fn call; plugin_free_fn free_buffer; void* shutdown; } plugin_api;
typedef int (*init_fn)(const host_api*, plugin_api*);

extern int hostCall(void*, char*, uint8_t*, size_t, cliproxy_buffer*);
extern void hostFree(void*, size_t);

static host_api host;
static plugin_api plugin;

static int load_plugin(const char* path) {
	void* lib = dlopen(path, RTLD_NOW);
	if (lib == NULL) return -1;
	init_fn init = (init_fn)dlsym(lib, "cliproxy_plugin_init");
	if (init == NULL) return -2;
	host.abi_version = 1;
	host.call = (host_call_fn)hostCall;
	host.free_buffer = hostFree;
	return init(&host, &plugin);
}

static int call_plugin(const char* method, const char* body, size_t len, cliproxy_buffer* out) {
	return plugin.call((char*)method, (uint8_t*)body, len, out);
}

static void free_plugin(void* ptr, size_t len) { plugin.free_buffer(ptr, len); }
*/
import "C"

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

//export hostCall
func hostCall(ctx unsafe.Pointer, method *C.char, request *C.uint8_t, requestLen C.size_t, out *C.cliproxy_buffer) C.int {
	_, _, _ = ctx, request, requestLen
	resp := `{"ok":true,"result":{}}`
	if C.GoString(method) == "host.auth.list" {
		resp = `{"ok":true,"result":{"files":[]}}`
	}
	out.ptr = C.CBytes([]byte(resp))
	out.len = C.size_t(len(resp))
	return 0
}

//export hostFree
func hostFree(ptr unsafe.Pointer, size C.size_t) {
	_ = size
	C.free(ptr)
}

func callPlugin(method, body string) string {
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	cBody := C.CString(body)
	defer C.free(unsafe.Pointer(cBody))
	var out C.cliproxy_buffer
	C.call_plugin(cMethod, cBody, C.size_t(len(body)), &out)
	resp := C.GoStringN((*C.char)(out.ptr), C.int(out.len))
	C.free_plugin(out.ptr, out.len)
	return resp
}

// recoveredFault dereferences nil and recovers, as an HTTP handler's recovery would.
func recoveredFault(count *atomic.Int64) {
	defer func() {
		if recover() != nil {
			count.Add(1)
		}
	}()
	var p *int
	_ = *p
}

func main() {
	duration := flag.Duration("for", 15*time.Second, "how long to run")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: faulthost [-for 15s] path/to/plugin")
		os.Exit(2)
	}
	path := C.CString(flag.Arg(0))
	defer C.free(unsafe.Pointer(path))
	if rc := C.load_plugin(path); rc != 0 {
		fmt.Fprintf(os.Stderr, "load plugin: %d\n", int(rc))
		os.Exit(1)
	}
	// config_yaml is base64 of "enabled: true\n".
	if resp := callPlugin("plugin.register", `{"config_yaml":"ZW5hYmxlZDogdHJ1ZQo="}`); !strings.HasPrefix(resp, `{"ok":true`) {
		fmt.Fprintf(os.Stderr, "register: %s\n", resp)
		os.Exit(1)
	}

	stop := time.Now().Add(*duration)
	var faults, calls atomic.Int64
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(3)
		go func() {
			defer wg.Done()
			for time.Now().Before(stop) {
				recoveredFault(&faults)
				time.Sleep(time.Millisecond)
			}
		}()
		go func() {
			defer wg.Done()
			for time.Now().Before(stop) {
				callPlugin("management.handle", `{"Method":"GET","Path":"/v0/management/claude-keep-rolling/state"}`)
				calls.Add(1)
			}
		}()
		// A busy loop plus GC makes the host send preemption signals.
		go func() {
			defer wg.Done()
			sum := 0
			for time.Now().Before(stop) {
				for i := range 50_000_000 {
					sum += i
				}
				runtime.GC()
			}
			_ = sum
		}()
	}
	wg.Wait()
	if faults.Load() == 0 || calls.Load() == 0 {
		fmt.Fprintf(os.Stderr, "no work done: %d faults, %d calls\n", faults.Load(), calls.Load())
		os.Exit(1)
	}
	fmt.Printf("ok: host recovered %d nil-pointer faults during %d plugin calls\n", faults.Load(), calls.Load())
}
