/* Shim: WebView2.h includes "EventToken.h" with Windows SDK casing. The
   mingw-w64 headers (used when cross-compiling) spell it "eventtoken.h", which
   a case-sensitive host filesystem cannot resolve, so forward to it. Windows
   filesystems and MSVC resolve the lowercase name case-insensitively. */
#include <eventtoken.h>
