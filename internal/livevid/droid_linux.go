//go:build linux

package livevid

/*
#cgo LDFLAGS: -ldl

#include "droidmedia.h"
#include <dlfcn.h>
#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include <time.h>
#include <unistd.h>

static const char *droid_step = "idle";

static void droid_trace(const char *s) {
	droid_step = s;
	int fd = open("/tmp/cargadgetd.log", O_WRONLY | O_APPEND | O_CLOEXEC);
	if (fd < 0)
		return;
	char buf[160];
	int n = snprintf(buf, sizeof(buf), "livevid: droid %s\n", s);
	if (n > 0) {
		ssize_t w = write(fd, buf, (size_t)n);
		(void)w;
	}
	close(fd);
}

static void droid_sigurg_state(void) {
	sigset_t cur;
	sigemptyset(&cur);
	pthread_sigmask(SIG_SETMASK, 0, &cur);
	droid_trace(sigismember(&cur, SIGURG) ? "sigurg blocked" : "sigurg NOT blocked");
}

static void *droid_h;

static bool (*p_init)(void);
static void (*p_deinit)(void);
static DroidMediaCodec *(*p_create_enc)(DroidMediaCodecEncoderMetaData *);
static bool (*p_is_supported)(DroidMediaCodecMetaData *, bool);
static unsigned int (*p_get_fmts)(DroidMediaCodecMetaData *, int, uint32_t *, unsigned int);
static void (*p_set_cb)(DroidMediaCodec *, DroidMediaCodecCallbacks *, void *);
static void (*p_set_data_cb)(DroidMediaCodec *, DroidMediaCodecDataCallbacks *, void *);
static bool (*p_start)(DroidMediaCodec *);
static void (*p_stop)(DroidMediaCodec *);
static void (*p_destroy)(DroidMediaCodec *);
static void (*p_queue)(DroidMediaCodec *, DroidMediaCodecData *, DroidMediaBufferCallbacks *);
static void (*p_flush)(DroidMediaCodec *);

typedef struct droid_au_out {
	uint8_t *data;
	int n;
	int sync;
	int cfg;
	int err;
	int slot_freed;
	uint8_t *cfg_data;
	int cfg_n;
} droid_au_out;

#define AU_Q 8

typedef struct au_item {
	uint8_t *data;
	int n;
	int sync;
	uint8_t *cfg;
	int cfg_n;
} au_item;

static pthread_mutex_t au_mu = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t au_cv = PTHREAD_COND_INITIALIZER;
static au_item auq[AU_Q];
static int auq_n;
static int au_err;
static uint8_t *cfg_pending;
static int cfg_pending_n;
static int slotq[8];
static int slotq_n;

static void au_reset_locked(void) {
	for (int i = 0; i < auq_n; i++) {
		free(auq[i].data);
		free(auq[i].cfg);
	}
	memset(auq, 0, sizeof(auq));
	auq_n = 0;
	au_err = 0;
	free(cfg_pending);
	cfg_pending = 0;
	cfg_pending_n = 0;
	slotq_n = 0;
}

struct droid_push_id {
	DroidMediaCodec *c;
	int slot;
};

static void c_error(void *data, int err) {
	(void)data;
	pthread_mutex_lock(&au_mu);
	au_err = err ? err : -1;
	pthread_cond_signal(&au_cv);
	pthread_mutex_unlock(&au_mu);
}
static void c_data(void *data, DroidMediaCodecData *enc) {
	(void)data;
	if (!enc)
		return;
	int n = (enc->data.data && enc->data.size > 0) ? (int)enc->data.size : 0;
	uint8_t *copy = 0;
	if (n > 0) {
		copy = (uint8_t *)malloc((size_t)n);
		if (copy)
			memcpy(copy, enc->data.data, (size_t)n);
	}
	pthread_mutex_lock(&au_mu);
	if (enc->codec_config) {
		free(cfg_pending);
		cfg_pending = copy;
		cfg_pending_n = copy ? n : 0;
	} else if (auq_n < AU_Q) {
		au_item *it = &auq[auq_n++];
		memset(it, 0, sizeof(*it));
		it->data = copy;
		it->n = copy ? n : 0;
		it->sync = enc->sync ? 1 : 0;
		if (cfg_pending) {
			it->cfg = cfg_pending;
			it->cfg_n = cfg_pending_n;
			cfg_pending = 0;
			cfg_pending_n = 0;
		}
		pthread_cond_signal(&au_cv);
	} else {
		free(copy);
	}
	pthread_mutex_unlock(&au_mu);
}
static void c_eos(void *data) { (void)data; }

static void c_unref(void *data) {
	struct droid_push_id *id = (struct droid_push_id *)data;
	if (!id)
		return;
	pthread_mutex_lock(&au_mu);
	if (slotq_n < 8)
		slotq[slotq_n++] = id->slot;
	pthread_cond_signal(&au_cv);
	pthread_mutex_unlock(&au_mu);
	free(id);
}

static __thread int on_worker;
static pthread_mutex_t wmu = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t wcv = PTHREAD_COND_INITIALIZER;
static pthread_cond_t wdone = PTHREAD_COND_INITIALIZER;
static void (*wfn)(void *);
static void *warg;
static int wready, wfinished, wstarted;
static pthread_t wthread;

static void *worker_main(void *p) {
	(void)p;
	on_worker = 1;
	pthread_mutex_lock(&wmu);
	for (;;) {
		while (!wready)
			pthread_cond_wait(&wcv, &wmu);
		void (*fn)(void *) = wfn;
		void *arg = warg;
		wready = 0;
		pthread_mutex_unlock(&wmu);
		fn(arg);
		pthread_mutex_lock(&wmu);
		wfinished = 1;
		pthread_cond_signal(&wdone);
	}
	return 0;
}

static void ensure_worker(void) {
	if (wstarted)
		return;
	pthread_attr_t a;
	pthread_attr_init(&a);
	pthread_attr_setstacksize(&a, 8u << 20);
	if (pthread_create(&wthread, &a, worker_main, 0) == 0)
		wstarted = 1;
	pthread_attr_destroy(&a);
}

static void call_worker(void (*fn)(void *), void *arg) {
	if (on_worker) {
		fn(arg);
		return;
	}
	pthread_mutex_lock(&wmu);
	ensure_worker();
	if (!wstarted) {
		pthread_mutex_unlock(&wmu);
		fn(arg);
		return;
	}
	wfn = fn;
	warg = arg;
	wready = 1;
	wfinished = 0;
	pthread_cond_signal(&wcv);
	while (!wfinished)
		pthread_cond_wait(&wdone, &wmu);
	pthread_mutex_unlock(&wmu);
}

static int droid_bind(void *h) {
	p_init = dlsym(h, "droid_media_init");
	p_deinit = dlsym(h, "droid_media_deinit");
	p_create_enc = dlsym(h, "droid_media_codec_create_encoder");
	p_is_supported = dlsym(h, "droid_media_codec_is_supported");
	p_get_fmts = dlsym(h, "droid_media_codec_get_supported_color_formats");
	p_set_cb = dlsym(h, "droid_media_codec_set_callbacks");
	p_set_data_cb = dlsym(h, "droid_media_codec_set_data_callbacks");
	p_start = dlsym(h, "droid_media_codec_start");
	p_stop = dlsym(h, "droid_media_codec_stop");
	p_destroy = dlsym(h, "droid_media_codec_destroy");
	p_queue = dlsym(h, "droid_media_codec_queue");
	p_flush = dlsym(h, "droid_media_codec_flush");
	if (!p_init || !p_create_enc || !p_start || !p_queue || !p_set_data_cb)
		return -1;
	return 0;
}

static const char *droid_load_body(void) {
	static const char *paths[] = {
		"libgstdroid-1.0.so.0",
		"/usr/lib64/libgstdroid-1.0.so.0",
		"/usr/lib/libgstdroid-1.0.so.0",
		0,
	};
	droid_sigurg_state();
	if (droid_h)
		return 0;
	droid_trace("dlopen");
	for (int i = 0; paths[i]; i++) {
		droid_h = dlopen(paths[i], RTLD_NOW | RTLD_GLOBAL);
		if (droid_h)
			break;
	}
	if (!droid_h)
		return dlerror();
	if (droid_bind(droid_h) != 0) {
		dlclose(droid_h);
		droid_h = 0;
		return "missing droid_media_* symbols";
	}
	if (!p_init()) {
		dlclose(droid_h);
		droid_h = 0;
		return "droid_media_init failed";
	}
	return 0;
}

struct droid_load_job { const char *err; };
static void droid_load_job_fn(void *p) {
	struct droid_load_job *j = (struct droid_load_job *)p;
	j->err = droid_load_body();
}
static const char *droid_load(void) {
	struct droid_load_job j;
	j.err = 0;
	call_worker(droid_load_job_fn, &j);
	return j.err;
}

static int droid_supported_body(void) {
	DroidMediaCodecMetaData md;
	memset(&md, 0, sizeof(md));
	md.type = "video/avc";
	md.flags = DROID_MEDIA_CODEC_HW_ONLY;
	if (!p_is_supported)
		return 1;
	if (p_is_supported(&md, 1))
		return 1;
	md.flags = 0;
	return p_is_supported(&md, 1) ? 1 : 0;
}

struct droid_sup_job { int out; };
static void droid_sup_job_fn(void *p) {
	((struct droid_sup_job *)p)->out = droid_supported_body();
}
static int droid_supported(void) {
	struct droid_sup_job j;
	call_worker(droid_sup_job_fn, &j);
	return j.out;
}

static unsigned int droid_fmts_body(uint32_t *out, unsigned int max) {
	DroidMediaCodecMetaData md;
	memset(&md, 0, sizeof(md));
	md.type = "video/avc";
	md.flags = DROID_MEDIA_CODEC_HW_ONLY;
	if (!p_get_fmts)
		return 0;
	return p_get_fmts(&md, 1, out, max);
}

struct droid_fmts_job {
	uint32_t *out;
	unsigned int max;
	unsigned int n;
};
static void droid_fmts_job_fn(void *p) {
	struct droid_fmts_job *j = (struct droid_fmts_job *)p;
	j->n = droid_fmts_body(j->out, j->max);
}
static unsigned int droid_fmts(uint32_t *out, unsigned int max) {
	struct droid_fmts_job j;
	j.out = out;
	j.max = max;
	j.n = 0;
	call_worker(droid_fmts_job_fn, &j);
	return j.n;
}

static DroidMediaCodec *droid_open_body(int w, int h, int fps, int bitrate, int color, int hw_only, int cbr) {
	pthread_mutex_lock(&au_mu);
	au_reset_locked();
	pthread_mutex_unlock(&au_mu);
	DroidMediaCodecEncoderMetaData md;
	memset(&md, 0, sizeof(md));
	md.parent.type = "video/avc";
	md.parent.width = w;
	md.parent.height = h;
	md.parent.fps = fps;
	md.parent.flags = hw_only ? DROID_MEDIA_CODEC_HW_ONLY : 0;
	md.color_format = color;
	md.bitrate = bitrate;
	md.meta_data = 0;
	md.stride = w;
	md.slice_height = h;
	md.max_input_size = w * h * 3 / 2;
	md.bitrate_mode = cbr ? DROID_MEDIA_CODEC_BITRATE_CONTROL_CBR : DROID_MEDIA_CODEC_BITRATE_CONTROL_VBR;
	md.codec_specific.h264.prepend_header_to_sync_frames = 1;
	md.codec_specific.h264.profile = 1;
	droid_trace("create");
	DroidMediaCodec *c = p_create_enc(&md);
	droid_trace(c ? "create ok" : "create nil");
	if (!c)
		return 0;
	DroidMediaCodecCallbacks cb;
	memset(&cb, 0, sizeof(cb));
	cb.signal_eos = c_eos;
	cb.error = c_error;
	p_set_cb(c, &cb, c);
	DroidMediaCodecDataCallbacks dcb;
	memset(&dcb, 0, sizeof(dcb));
	dcb.data_available = c_data;
	p_set_data_cb(c, &dcb, c);
	droid_trace("start");
	if (!p_start(c)) {
		droid_trace("start failed");
		p_destroy(c);
		return 0;
	}
	droid_trace("start ok");
	return c;
}

struct droid_open_job {
	int w, h, fps, bitrate, color, hw_only, cbr;
	DroidMediaCodec *out;
};
static void droid_open_job_fn(void *p) {
	struct droid_open_job *j = (struct droid_open_job *)p;
	j->out = droid_open_body(j->w, j->h, j->fps, j->bitrate, j->color, j->hw_only, j->cbr);
}
static DroidMediaCodec *droid_open(int w, int h, int fps, int bitrate, int color, int hw_only, int cbr) {
	struct droid_open_job j;
	memset(&j, 0, sizeof(j));
	j.w = w;
	j.h = h;
	j.fps = fps;
	j.bitrate = bitrate;
	j.color = color;
	j.hw_only = hw_only;
	j.cbr = cbr;
	call_worker(droid_open_job_fn, &j);
	return j.out;
}

static DroidMediaCodec *droid_open_alt_body(int w, int h, int fps, int bitrate, int color, int hw_only, int cbr) {
	pthread_mutex_lock(&au_mu);
	au_reset_locked();
	pthread_mutex_unlock(&au_mu);
	DroidMediaCodecEncoderMetaDataAlt md;
	memset(&md, 0, sizeof(md));
	md.parent.type = "video/avc";
	md.parent.width = w;
	md.parent.height = h;
	md.parent.fps = fps;
	md.parent.flags = hw_only ? DROID_MEDIA_CODEC_HW_ONLY : 0;
	md.parent.stride = w;
	md.parent.slice_height = h;
	md.color_format = color;
	md.bitrate = bitrate;
	md.meta_data = 0;
	md.stride = w;
	md.slice_height = h;
	md.max_input_size = w * h * 3 / 2;
	md.bitrate_mode = cbr ? DROID_MEDIA_CODEC_BITRATE_CONTROL_CBR : DROID_MEDIA_CODEC_BITRATE_CONTROL_VBR;
	md.codec_specific.h264.prepend_header_to_sync_frames = 1;
	md.codec_specific.h264.profile = 1;
	droid_trace("create alt");
	DroidMediaCodec *c = p_create_enc((DroidMediaCodecEncoderMetaData *)&md);
	droid_trace(c ? "create alt ok" : "create alt nil");
	if (!c)
		return 0;
	DroidMediaCodecCallbacks cb;
	memset(&cb, 0, sizeof(cb));
	cb.signal_eos = c_eos;
	cb.error = c_error;
	p_set_cb(c, &cb, c);
	DroidMediaCodecDataCallbacks dcb;
	memset(&dcb, 0, sizeof(dcb));
	dcb.data_available = c_data;
	p_set_data_cb(c, &dcb, c);
	droid_trace("start alt");
	if (!p_start(c)) {
		droid_trace("start alt failed");
		p_destroy(c);
		return 0;
	}
	droid_trace("start alt ok");
	return c;
}

struct droid_open_alt_job {
	int w, h, fps, bitrate, color, hw_only, cbr;
	DroidMediaCodec *out;
};
static void droid_open_alt_job_fn(void *p) {
	struct droid_open_alt_job *j = (struct droid_open_alt_job *)p;
	j->out = droid_open_alt_body(j->w, j->h, j->fps, j->bitrate, j->color, j->hw_only, j->cbr);
}
static DroidMediaCodec *droid_open_alt(int w, int h, int fps, int bitrate, int color, int hw_only, int cbr) {
	struct droid_open_alt_job j;
	memset(&j, 0, sizeof(j));
	j.w = w;
	j.h = h;
	j.fps = fps;
	j.bitrate = bitrate;
	j.color = color;
	j.hw_only = hw_only;
	j.cbr = cbr;
	call_worker(droid_open_alt_job_fn, &j);
	return j.out;
}

static int droid_submit_body(DroidMediaCodec *c, void *yuv, int size, int64_t ts, int sync, int slot) {
	DroidMediaCodecData d;
	memset(&d, 0, sizeof(d));
	d.data.data = yuv;
	d.data.size = size;
	d.ts = ts;
	d.sync = sync ? true : false;
	struct droid_push_id *id = (struct droid_push_id *)malloc(sizeof(*id));
	if (!id)
		return -1;
	id->c = c;
	id->slot = slot;
	DroidMediaBufferCallbacks cb;
	memset(&cb, 0, sizeof(cb));
	cb.unref = c_unref;
	cb.data = id;
	p_queue(c, &d, &cb);
	return 0;
}

static int droid_take_body(droid_au_out *out) {
	struct timespec deadline;
	clock_gettime(CLOCK_REALTIME, &deadline);
	deadline.tv_nsec += 400000000L;
	if (deadline.tv_nsec >= 1000000000L) {
		deadline.tv_sec++;
		deadline.tv_nsec -= 1000000000L;
	}
	pthread_mutex_lock(&au_mu);
	while (auq_n == 0 && !au_err) {
		if (pthread_cond_timedwait(&au_cv, &au_mu, &deadline) == ETIMEDOUT)
			break;
	}
	memset(out, 0, sizeof(*out));
	if (auq_n > 0) {
		au_item it = auq[0];
		memmove(&auq[0], &auq[1], (size_t)(auq_n - 1) * sizeof(au_item));
		auq_n--;
		memset(&auq[auq_n], 0, sizeof(au_item));
		out->data = it.data;
		out->n = it.n;
		out->sync = it.sync;
		out->cfg_data = it.cfg;
		out->cfg_n = it.cfg_n;
		out->cfg = it.cfg ? 1 : 0;
		pthread_mutex_unlock(&au_mu);
		return 0;
	}
	out->err = au_err ? au_err : -1;
	au_err = 0;
	pthread_mutex_unlock(&au_mu);
	return -1;
}

static int droid_poll_slots(int *out, int max) {
	pthread_mutex_lock(&au_mu);
	int n = slotq_n;
	if (n > max)
		n = max;
	for (int i = 0; i < n; i++)
		out[i] = slotq[i];
	int left = slotq_n - n;
	for (int i = 0; i < left; i++)
		slotq[i] = slotq[n + i];
	slotq_n = left;
	pthread_mutex_unlock(&au_mu);
	return n;
}

struct droid_submit_job {
	DroidMediaCodec *c;
	void *yuv;
	int size;
	int64_t ts;
	int sync;
	int slot;
	int rc;
};
static void droid_submit_job_fn(void *p) {
	struct droid_submit_job *j = (struct droid_submit_job *)p;
	j->rc = droid_submit_body(j->c, j->yuv, j->size, j->ts, j->sync, j->slot);
}
static int droid_submit(DroidMediaCodec *c, void *yuv, int size, int64_t ts, int sync, int slot) {
	struct droid_submit_job j;
	memset(&j, 0, sizeof(j));
	j.c = c;
	j.yuv = yuv;
	j.size = size;
	j.ts = ts;
	j.sync = sync;
	j.slot = slot;
	call_worker(droid_submit_job_fn, &j);
	return j.rc;
}

struct droid_take_job {
	droid_au_out *out;
	int rc;
};
static void droid_take_job_fn(void *p) {
	struct droid_take_job *j = (struct droid_take_job *)p;
	j->rc = droid_take_body(j->out);
}
static int droid_take(droid_au_out *out) {
	struct droid_take_job j;
	j.out = out;
	j.rc = -1;
	call_worker(droid_take_job_fn, &j);
	return j.rc;
}

struct droid_poll_job {
	int *out;
	int max;
	int n;
};
static void droid_poll_job_fn(void *p) {
	struct droid_poll_job *j = (struct droid_poll_job *)p;
	j->n = droid_poll_slots(j->out, j->max);
}
static int droid_poll(int *out, int max) {
	struct droid_poll_job j;
	j.out = out;
	j.max = max;
	j.n = 0;
	call_worker(droid_poll_job_fn, &j);
	return j.n;
}

static void droid_close_body(DroidMediaCodec *c) {
	if (!c)
		return;
	p_stop(c);
	p_destroy(c);
	pthread_mutex_lock(&au_mu);
	au_reset_locked();
	pthread_mutex_unlock(&au_mu);
}
struct droid_close_job { DroidMediaCodec *c; };
static void droid_close_job_fn(void *p) {
	droid_close_body(((struct droid_close_job *)p)->c);
}
static void droid_close(DroidMediaCodec *c) {
	struct droid_close_job j;
	j.c = c;
	call_worker(droid_close_job_fn, &j);
}
*/
import "C"

import (
	"fmt"
	"image"
	"log"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// OMX / Android color formats we can produce from RGBA.
const (
	omxYUV420Planar     = 19
	omxYUV420SemiPlanar = 21
	omxYUV420PackedSP   = 39
	qcomYUV420SP        = 0x7FA30C00
	tiYUV420PackedSP    = 0x7F000100
)

type droidEnc struct {
	name    string
	codec   unsafe.Pointer
	w, h    int
	layout  int
	ptr     [droidSlots]unsafe.Pointer
	yuv     [droidSlots][]byte
	busy    [droidSlots]uint32
	free    chan struct{}
	mu      sync.Mutex
	out     chan []byte
	errc    chan error
	cfg     []byte
	nalLen  int
	seq     int64
	first   uint32
	pending int  // submitted frames whose AU has not been returned
	holdPS  bool // alternative encoder: a leading SPS/PPS buffer is not a failed frame
}

// Frames left in the codec when Encode returns. Two lets the next color
// conversion finish before the older access unit is due, which is what
// keeps 1080p inside a 16.7ms frame. droidSlots must be deeper than that
// so the conversion has a free buffer.
const (
	droidSlots = 4
	droidPipe  = 2
)

var droidTab sync.Map // codec pointer → *droidEnc

func newDroid(w, h, fps, bps int) (codec, error) {
	return openDroid(w, h, fps, bps, false)
}

func newDroidAlt(w, h, fps, bps int) (codec, error) {
	return openDroid(w, h, fps, bps, true)
}

func openDroid(w, h, fps, bps int, alt bool) (codec, error) {
	var out codec
	var err error
	// Hold SIGURG blocked across create/start so Codec2's threads inherit the mask.
	withoutAsyncPreempt(func() {
		out, err = newDroidIn(w, h, fps, bps, alt)
	})
	return out, err
}

func newDroidIn(w, h, fps, bps int, alt bool) (codec, error) {
	if err := C.droid_load(); err != nil {
		return nil, fmt.Errorf("dlopen: %s", C.GoString(err))
	}
	if C.droid_supported() == 0 {
		return nil, fmt.Errorf("no video/avc encoder")
	}
	var fmts [16]C.uint32_t
	n := int(C.droid_fmts(&fmts[0], 16))
	color, layout := pickDroidColor(fmts[:n])
	if color == 0 {
		// Common defaults when the HAL does not enumerate.
		color, layout = omxYUV420SemiPlanar, yuvNV12
	}
	if bps < minBps {
		bps = minBps
	}
	cbps := C.int(bps)
	var c *C.DroidMediaCodec
	name := "droidmedia-hw"
	kind := "droidmedia"
	if alt {
		name = "droidmedia-alt"
		kind = "droidmedia-alt"
		c = C.droid_open_alt(C.int(w), C.int(h), C.int(fps), cbps, C.int(color), 1, 1)
		if c == nil {
			c = C.droid_open_alt(C.int(w), C.int(h), C.int(fps), cbps, C.int(color), 1, 0)
		}
		if c == nil {
			c = C.droid_open_alt(C.int(w), C.int(h), C.int(fps), cbps, C.int(color), 0, 1)
		}
		if c == nil {
			// Same attempts as Hardware, if this droidmedia is the older layout.
			kind = "droidmedia-alt-oldabi"
			c = C.droid_open(C.int(w), C.int(h), C.int(fps), cbps, C.int(color), 1, 1)
			if c == nil {
				c = C.droid_open(C.int(w), C.int(h), C.int(fps), cbps, C.int(color), 1, 0)
			}
			if c == nil {
				c = C.droid_open(C.int(w), C.int(h), C.int(fps), cbps, C.int(color), 0, 1)
			}
		}
	} else {
		c = C.droid_open(C.int(w), C.int(h), C.int(fps), cbps, C.int(color), 1, 1)
		if c == nil {
			c = C.droid_open(C.int(w), C.int(h), C.int(fps), cbps, C.int(color), 1, 0)
		}
		if c == nil {
			c = C.droid_open(C.int(w), C.int(h), C.int(fps), cbps, C.int(color), 0, 1)
		}
	}
	if c == nil {
		return nil, fmt.Errorf("create_encoder failed (color=0x%x nfmt=%d)", color, n)
	}
	log.Printf("livevid: %s color=0x%x layout=%d %dx%d @%dfps %dbps nfmt=%d", kind, color, layout, w, h, fps, bps, n)
	sz := yuvSize(w, h)
	e := &droidEnc{
		name:   name,
		codec:  unsafe.Pointer(c),
		w:      w,
		h:      h,
		layout: layout,
		free:   make(chan struct{}, droidSlots),
		out:    make(chan []byte, 8),
		errc:   make(chan error, 1),
		nalLen: 4,
		holdPS: alt,
	}
	droidTab.Store(e.codec, e)
	for i := 0; i < droidSlots; i++ {
		p := C.malloc(C.size_t(sz))
		if p == nil {
			e.Close()
			return nil, fmt.Errorf("droid yuv malloc")
		}
		e.ptr[i] = p
		e.yuv[i] = unsafe.Slice((*byte)(p), sz)
	}
	return e, nil
}

func pickDroidColor(fmts []C.uint32_t) (color int, layout int) {
	prefer := []struct {
		c int
		l int
	}{
		{omxYUV420SemiPlanar, yuvNV12},
		{qcomYUV420SP, yuvNV12},
		{omxYUV420PackedSP, yuvNV12},
		{tiYUV420PackedSP, yuvNV12},
		{omxYUV420Planar, yuvI420},
	}
	have := map[uint32]bool{}
	for _, f := range fmts {
		have[uint32(f)] = true
	}
	if len(fmts) == 0 {
		return omxYUV420SemiPlanar, yuvNV12
	}
	for _, p := range prefer {
		if have[uint32(p.c)] {
			return p.c, p.l
		}
	}
	return int(fmts[0]), yuvNV12
}

// CodecConfig returns the SPS/PPS the encoder reported, Annex-B, ready to be
// sent as a CODEC_CONFIG message.
func (e *droidEnc) CodecConfig() []byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.cfg) == 0 {
		return nil
	}
	return append([]byte(nil), e.cfg...)
}

func (e *droidEnc) Name() string {
	if e.name != "" {
		return e.name
	}
	return "droidmedia-hw"
}

func (e *droidEnc) acquire() (int, error) {
	deadline := time.Now().Add(400 * time.Millisecond)
	for {
		for i := 0; i < droidSlots; i++ {
			if atomic.CompareAndSwapUint32(&e.busy[i], 0, 1) {
				return i, nil
			}
		}
		d := time.Until(deadline)
		if d <= 0 {
			return 0, fmt.Errorf("droid input buffer timeout")
		}
		select {
		case <-e.free:
		case <-time.After(d):
			return 0, fmt.Errorf("droid input buffer timeout")
		}
	}
}

func (e *droidEnc) release(slot int) {
	if slot < 0 || slot >= droidSlots {
		return
	}
	atomic.StoreUint32(&e.busy[slot], 0)
	select {
	case e.free <- struct{}{}:
	default:
	}
}

// Encode submits img and returns an access unit from droidPipe frames ago.
// The first droidPipe calls return (nil, nil) while those frames stay in
// the codec. Callers skip an empty slice.
func (e *droidEnc) Encode(img *image.RGBA) ([]byte, error) {
	split := OnPhase != nil
	e.drainSlots()
	slot, err := e.acquire()
	if err != nil {
		return nil, err
	}
	var t0 time.Time
	if split {
		t0 = time.Now()
	}
	rgbaToYUV(e.yuv[slot], img, e.layout)
	var yuvTook time.Duration
	if split {
		yuvTook = time.Since(t0)
		t0 = time.Now()
	}
	e.mu.Lock()
	e.seq++
	ts := e.seq * 33333
	e.mu.Unlock()
	sync := 0
	if atomic.CompareAndSwapUint32(&e.first, 0, 1) {
		sync = 1
	}
	var pushErr C.int
	withoutAsyncPreempt(func() {
		pushErr = C.droid_submit((*C.DroidMediaCodec)(e.codec), e.ptr[slot], C.int(len(e.yuv[slot])), C.int64_t(ts), C.int(sync), C.int(slot))
	})
	var subTook time.Duration
	if split {
		subTook = time.Since(t0)
	}
	if pushErr != 0 {
		e.release(slot)
		return nil, fmt.Errorf("droid queue failed")
	}
	e.drainSlots()
	e.pending++
	if e.pending <= droidPipe {
		if split {
			OnPhase(Phase{YUV: yuvTook, Submit: subTook})
		}
		return nil, nil
	}
	if split {
		t0 = time.Now()
	}
	au, err := e.takeAU()
	if err != nil {
		return nil, err
	}
	e.pending--
	if split {
		OnPhase(Phase{YUV: yuvTook, Submit: subTook, Take: time.Since(t0)})
	}
	return au, nil
}

func (e *droidEnc) drainSlots() {
	var slots [droidSlots]C.int
	var n C.int
	withoutAsyncPreempt(func() {
		n = C.droid_poll(&slots[0], C.int(droidSlots))
	})
	for i := 0; i < int(n); i++ {
		e.release(int(slots[i]))
	}
}

func (e *droidEnc) takeAU() ([]byte, error) {
	// Hardware returns the first buffer as-is. Unisoc's first buffer is
	// SPS/PPS with no slice (empty AU). The alternative encoder waits for
	// the picture that follows, inside this same call.
	tries := 1
	if e.holdPS {
		tries = 4
	}
	for try := 0; try < tries; try++ {
		au, err := e.takeOnce()
		if err != nil {
			return nil, err
		}
		if len(au) > 0 {
			return au, nil
		}
		if !e.holdPS {
			return nil, fmt.Errorf("empty AU")
		}
	}
	return nil, fmt.Errorf("empty AU")
}

func (e *droidEnc) takeOnce() ([]byte, error) {
	var out C.droid_au_out
	var rc C.int
	withoutAsyncPreempt(func() {
		rc = C.droid_take(&out)
	})
	e.drainSlots()
	if out.cfg_data != nil && out.cfg_n > 0 {
		cfg := C.GoBytes(unsafe.Pointer(out.cfg_data), out.cfg_n)
		C.free(unsafe.Pointer(out.cfg_data))
		e.accept(cfg, false, true)
	}
	var raw []byte
	if out.data != nil && out.n > 0 {
		raw = C.GoBytes(unsafe.Pointer(out.data), out.n)
		C.free(unsafe.Pointer(out.data))
	}
	if rc != 0 && len(raw) == 0 {
		if out.err != 0 && out.err != -1 {
			return nil, fmt.Errorf("droid codec error 0x%x", uint32(out.err))
		}
		return nil, fmt.Errorf("droid encoder timeout")
	}
	return e.accept(raw, out.sync != 0, false), nil
}

func (e *droidEnc) Close() error {
	for e.pending > 0 {
		if _, err := e.takeAU(); err != nil {
			e.pending = 0
			break
		}
		e.pending--
	}
	if e.codec != nil {
		droidTab.Delete(e.codec)
		codec := e.codec
		e.codec = nil
		withoutAsyncPreempt(func() {
			C.droid_close((*C.DroidMediaCodec)(codec))
		})
	}
	for i := 0; i < droidSlots; i++ {
		if e.ptr[i] != nil {
			C.free(e.ptr[i])
			e.ptr[i] = nil
			e.yuv[i] = nil
		}
	}
	return nil
}

func droidLookup(p unsafe.Pointer) *droidEnc {
	v, ok := droidTab.Load(p)
	if !ok {
		return nil
	}
	return v.(*droidEnc)
}

//export droidGoError
func droidGoError(data unsafe.Pointer, err C.int) {
	e := droidLookup(data)
	if e == nil {
		return
	}
	select {
	case e.errc <- fmt.Errorf("droid codec error 0x%x", uint32(-err)):
	default:
	}
}

//export droidGoEOS
func droidGoEOS(data unsafe.Pointer) {}

//export droidGoUnrefSlot
func droidGoUnrefSlot(codec unsafe.Pointer, slot C.int) {
	e := droidLookup(codec)
	if e == nil {
		return
	}
	e.release(int(slot))
}

func (e *droidEnc) accept(raw []byte, sync, cfgFlag bool) []byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	cfg := cfgFlag || parameterSetsOnly(raw)
	if cfg {
		e.cfg = append([]byte(nil), raw...)
		if _, nsz, consumed := parseAvcC(raw); consumed > 0 {
			e.nalLen = nsz
		}
		head := raw
		if len(head) > 24 {
			head = head[:24]
		}
		flag := "flag"
		if !cfgFlag {
			flag = "inferred"
		}
		log.Printf("livevid: droid codec_config %s %dB nalLen=%d head=%x", flag, len(raw), e.nalLen, head)
		return nil
	}
	return toAnnexBSized(raw, e.cfg, sync, e.nalLen)
}

//export droidGoData
func droidGoData(data unsafe.Pointer, encoded *C.DroidMediaCodecData) {
	e := droidLookup(data)
	if e == nil || encoded == nil {
		return
	}
	n := int(encoded.data.size)
	var raw []byte
	if n > 0 && encoded.data.data != nil {
		raw = C.GoBytes(encoded.data.data, C.int(n))
	}
	au := e.accept(raw, bool(encoded.sync), bool(encoded.codec_config))
	if au == nil {
		return
	}
	select {
	case e.out <- au:
	default:
		log.Printf("livevid: droid AU dropped (%dB sync=%v)", len(au), bool(encoded.sync))
	}
}
