/*
 * aviflph.h - C API for the aviflph Live Photo converter.
 *
 * Provides programmatic access to Motion Photo (JPEG) -> AVIF Live Photo
 * conversion, AVIF demux/meta inspection and other utilities exposed by
 * the libaviflph shared library (libaviflph.so / libaviflph.dylib /
 * aviflph.dll).
 *
 * All dynamically allocated outputs returned by this library must be
 * released with lp_free(). The error buffer returned by lp_last_error()
 * is internal; copy the string if you need to keep it past the next call.
 *
 * Thread safety: every function except lp_last_error() is safe to call
 * concurrently from different goroutines/threads. lp_last_error() reads
 * a shared buffer that may be overwritten by another concurrent call.
 */

#ifndef AVIFLPH_H
#define AVIFLPH_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#if defined(_WIN32)
#  if defined(AVIFLPH_BUILD)
#    define AVIFLPH_API __declspec(dllexport)
#  else
#    define AVIFLPH_API __declspec(dllimport)
#  endif
#else
#  define AVIFLPH_API
#endif

/* Returns the library version string (static, do not free). */
AVIFLPH_API const char *lp_version(void);

/* Returns a pointer to the most recent error message buffer.
 * The buffer is internal; it stays valid only until the next call into
 * the library. Copy it if you need a longer lifetime. */
AVIFLPH_API const char *lp_last_error(void);

/* Frees a buffer previously allocated by this library (lp_convert,
 * lp_getmeta, lp_demux, lp_embed, lp_extract_*). Passing NULL is OK. */
AVIFLPH_API void lp_free(void *ptr);

/* Full pipeline: detect input format, decode/encode as needed, and emit
 * an xomu Live Photo AVIF.
 *
 *   src           : JPEG Motion Photo / static AVIF / MP4 (may be NULL
 *                   when both still_avif and video_mp4 are supplied)
 *   src_len       : byte length of src
 *   still_avif    : pre-encoded still AVIF bytes (NULL if src encodes it)
 *   still_avif_len: byte length of still_avif
 *   video_mp4     : pre-encoded video MP4 bytes (NULL if src encodes it)
 *   video_mp4_len : byte length of video_mp4
 *   quality       : AVIF still quality (1-100, 0 -> default)
 *   crf           : AV1 video CRF (0-63, 0 -> default)
 *   raw           : non-zero to keep source media untouched
 *                   (JPEG sources are embedded as-is, no AVIF encode;
 *                   video is muxed without re-encode)
 *   out           : receives the allocated output bytes (free with lp_free)
 *   out_len       : receives the byte length of the output
 *
 * Returns 0 on success and -1 on failure (call lp_last_error()).
 */
AVIFLPH_API int lp_convert(
    const void *src, size_t src_len,
    const void *still_avif, size_t still_avif_len,
    const void *video_mp4, size_t video_mp4_len,
    int quality, int crf, int raw,
    void **out, size_t *out_len);

/* Pure pack: combine a pre-encoded still AVIF and pre-encoded MP4 video
 * into an xomu Live Photo AVIF. No decoding or encoding is performed.
 * out / out_len must be released with lp_free. Returns 0 on success. */
AVIFLPH_API int lp_embed(
    const void *still, size_t still_len,
    const void *video, size_t video_len,
    void **out, size_t *out_len);

/* Returns a human-readable meta report for the input (text by default,
 * JSON when as_json is non-zero). The returned string must be released
 * with lp_free. */
AVIFLPH_API char *lp_getmeta(const void *src, size_t src_len, int as_json);

/* Split an input (JPEG Motion Photo or xomu Live Photo AVIF) into its
 * still image and video components. All output buffers and extension
 * strings must be released with lp_free. Returns 0 on success. */
AVIFLPH_API int lp_demux(
    const void *src, size_t src_len,
    void **still, size_t *still_len, char **still_ext,
    void **video, size_t *video_len, char **video_ext);

/* Extract the still image from any supported input. out / out_len / ext
 * must be released with lp_free. Returns 0 on success. */
AVIFLPH_API int lp_extract_still(
    const void *src, size_t src_len,
    void **out, size_t *out_len, char **ext);

/* Extract the embedded video from any supported input. out / out_len
 * must be released with lp_free. Returns 0 on success. */
AVIFLPH_API int lp_extract_video(
    const void *src, size_t src_len,
    void **out, size_t *out_len);

#ifdef __cplusplus
}
#endif

#endif /* AVIFLPH_H */
