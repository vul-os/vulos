# The frame has no @JavascriptInterface bridge today (the shell talks to its
# instance over the network, not through a native bridge). If one is ever added,
# R8 would strip the reflectively-called methods — keep them:
-keepclassmembers class org.vulos.mobile.** {
    @android.webkit.JavascriptInterface <methods>;
}

# androidx.webkit uses reflection to detect optional WebView features.
-keep class androidx.webkit.** { *; }
