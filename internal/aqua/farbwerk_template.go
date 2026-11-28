package aqua

// fwTemplate is the Farbwerk config the plugin starts from when the device won't return its
// config (the original Farbwerk stalls GET_FEATURE 0x03; aquasuite never reads it either) and
// no saved copy exists yet. It is the user's own last captured aquasuite write (2026-11-27,
// testdata/farbwerk_writes.txt "options2-7") with RGB output 4's strip type set back to
// 0 = "12V, Green, Red, Blue", the strip type in use.
const fwTemplateHex = "0300041404d20000000000000000000000000832000f00000000062e0fff0fff06300fff0fffff64870000000208041d056c053103ba0208000000000000000000000a320010000000000aaa0fff0fff0fff0fff0fffff64870000000208041d056c053103ba02080000000000000000000000320010000000000aaa0fff0fff0fff0fff0fffff64870000000208041d056c053103ba02080000000000000000000004320010000027100aaa0fff0fff00000fff0fffff64870000000208041d056c053103ba02080000000000000000000001f4068201061388064001061388064001061388064001060182ae"
