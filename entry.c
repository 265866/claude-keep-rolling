// Entry points between the CLIProxyAPI host and this plugin's Go runtime. On most
// platforms they only forward calls. On darwin/amd64 they also keep the two Go
// runtimes in one process from mistaking each other's state for their own.
#include "abi.h"
#include "_cgo_export.h"

#if defined(__APPLE__) && defined(__x86_64__)
#include <signal.h>

// Every Go runtime on darwin/amd64 keeps the current g in the fixed TLS slot
// %gs:0x30, so the host (also Go) and this plugin share one slot. Entering one
// runtime with the other's g in the slot crashes it ("fatal error: unknown caller
// pc"). Each crossing therefore parks the caller's g and puts back the g the
// callee left on this thread last time: its bound g0 after a callback, or NULL
// on first entry, which cgo handles by binding an extra M to the thread. Handing
// the callee its own g0 again lets it reuse that M; clearing the slot every time
// would bind and leak a new M per crossing.
static __thread void* host_g;
static __thread void* plugin_g;

static inline void* load_g(void) {
	void* g;
	__asm__ volatile("movq %%gs:0x30, %0" : "=r"(g) : : "memory");
	return g;
}

static inline void store_g(void* g) {
	__asm__ volatile("movq %0, %%gs:0x30" : : "r"(g) : "memory");
}

typedef struct {
	void* caller_g;
	void* parked;
} crossing;

// enter_plugin runs on a host thread before calling into the plugin. A nested call
// back to the host gets the host's own g0, as for an ordinary Go-to-C-to-Go callback.
static inline crossing enter_plugin(void) {
	crossing c = {load_g(), host_g};
	host_g = c.caller_g;
	store_g(plugin_g);
	return c;
}

static inline void leave_plugin(crossing c) {
	plugin_g = load_g();
	host_g = c.parked;
	store_g(c.caller_g);
}

static inline crossing enter_host(void) {
	crossing c = {load_g(), plugin_g};
	plugin_g = c.caller_g;
	store_g(host_g);
	return c;
}

static inline void leave_host(crossing c) {
	host_g = load_g();
	// In a nested plugin-to-host call this restores a stale plugin_g; the outer
	// leave_plugin reads the slot again before anything uses it.
	plugin_g = c.parked;
	store_g(c.caller_g);
}

// While loading, this plugin's runtime takes over the fault, SIGPIPE and
// preemption signals. Its handler reads the shared slot, so a fault in host code
// would be handled as the plugin's own and become fatal instead of a recoverable
// panic. Go installs one handler for every signal it handles and the plugin leaves
// SIGCHLD alone, so SIGCHLD still holds the host's handler: copy it back. Faults
// in plugin code then reach the host's handler instead, and the plugin loses
// async preemption. Its preemption bookkeeping then never settles, so the plugin
// must not fork or exec (no os/exec).
static void return_signals_to_host(void) {
	struct sigaction host;
	if (sigaction(SIGCHLD, NULL, &host) != 0 || !(host.sa_flags & SA_SIGINFO) || host.sa_sigaction == NULL) {
		return;
	}
	const int taken[] = {SIGSEGV, SIGBUS, SIGFPE, SIGPIPE, SIGURG};
	for (size_t i = 0; i < sizeof(taken) / sizeof(taken[0]); i++) {
		sigaction(taken[i], &host, NULL);
	}
}
#else
static void return_signals_to_host(void) {}
typedef int crossing;
static inline crossing enter_plugin(void) { return 0; }
static inline void leave_plugin(crossing c) { (void)c; }
static inline crossing enter_host(void) { return 0; }
static inline void leave_host(crossing c) { (void)c; }
#endif

#ifdef _WIN32
#define ENTRY_EXPORT __declspec(dllexport)
#else
#define ENTRY_EXPORT __attribute__((visibility("default")))
#endif

static const cliproxy_host_api* stored_host;

ENTRY_EXPORT int cliproxy_plugin_init(const cliproxy_host_api* host, cliproxy_plugin_api* plugin) {
	return_signals_to_host();
	crossing c = enter_plugin();
	int rc = cliproxyPluginInit((cliproxy_host_api*)host, plugin);
	leave_plugin(c);
	return rc;
}

ENTRY_INTERNAL int entry_plugin_call(char* method, uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	crossing c = enter_plugin();
	int rc = cliproxyPluginCall(method, request, request_len, response);
	leave_plugin(c);
	return rc;
}

ENTRY_INTERNAL void entry_plugin_free(void* ptr, size_t len) {
	(void)len;
	free(ptr);
}

ENTRY_INTERNAL void entry_plugin_shutdown(void) {
	crossing c = enter_plugin();
	cliproxyPluginShutdown();
	leave_plugin(c);
}

ENTRY_INTERNAL void entry_store_host(const cliproxy_host_api* host) {
	stored_host = host;
}

ENTRY_INTERNAL int entry_call_host(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	crossing c = enter_host();
	int rc = stored_host->call(stored_host->host_ctx, method, request, request_len, response);
	leave_host(c);
	return rc;
}

ENTRY_INTERNAL void entry_free_host_buffer(void* ptr, size_t len) {
	if (stored_host == NULL || stored_host->free_buffer == NULL || ptr == NULL) {
		return;
	}
	crossing c = enter_host();
	stored_host->free_buffer(ptr, len);
	leave_host(c);
}
