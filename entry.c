// Entry points between the CLIProxyAPI host and this plugin's Go runtime.
#include "abi.h"
#include "_cgo_export.h"

#if defined(__APPLE__) && defined(__x86_64__)
// Every Go runtime on darwin/amd64 keeps the current goroutine in the fixed TLS
// slot %gs:0x30, so the host (also Go) and this plugin share one slot. Entering
// one runtime from a thread owned by the other would hand it a foreign goroutine
// ("fatal error: unknown caller pc"). Clearing the slot for the duration of each
// crossing makes the callee treat the thread as a plain C thread, which Go supports.
static inline void* detach_goroutine(void) {
	void* g;
	__asm__ volatile("movq %%gs:0x30, %0" : "=r"(g));
	__asm__ volatile("movq %0, %%gs:0x30" : : "r"((void*)0) : "memory");
	return g;
}

static inline void reattach_goroutine(void* g) {
	__asm__ volatile("movq %0, %%gs:0x30" : : "r"(g) : "memory");
}
#else
static inline void* detach_goroutine(void) { return NULL; }
static inline void reattach_goroutine(void* g) { (void)g; }
#endif

#ifdef _WIN32
#define ENTRY_EXPORT __declspec(dllexport)
#else
#define ENTRY_EXPORT __attribute__((visibility("default")))
#endif

static const cliproxy_host_api* stored_host;

ENTRY_EXPORT int cliproxy_plugin_init(const cliproxy_host_api* host, cliproxy_plugin_api* plugin) {
	void* g = detach_goroutine();
	int rc = cliproxyPluginInit((cliproxy_host_api*)host, plugin);
	reattach_goroutine(g);
	return rc;
}

int entry_plugin_call(char* method, uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	void* g = detach_goroutine();
	int rc = cliproxyPluginCall(method, request, request_len, response);
	reattach_goroutine(g);
	return rc;
}

void entry_plugin_free(void* ptr, size_t len) {
	(void)len;
	free(ptr);
}

void entry_plugin_shutdown(void) {
	void* g = detach_goroutine();
	cliproxyPluginShutdown();
	reattach_goroutine(g);
}

void entry_store_host(const cliproxy_host_api* host) {
	stored_host = host;
}

int entry_call_host(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	void* g = detach_goroutine();
	int rc = stored_host->call(stored_host->host_ctx, method, request, request_len, response);
	reattach_goroutine(g);
	return rc;
}

void entry_free_host_buffer(void* ptr, size_t len) {
	if (stored_host == NULL || stored_host->free_buffer == NULL || ptr == NULL) {
		return;
	}
	void* g = detach_goroutine();
	stored_host->free_buffer(ptr, len);
	reattach_goroutine(g);
}
