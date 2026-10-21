//go:build android || !(linux || ((darwin || windows) && !arm && !386 && !ios))

package model

func writeClipboardImage([]byte) error {
	return errClipboardPlatformUnsupported
}
