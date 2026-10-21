//go:build !(unix && !darwin && !android) && !((darwin || windows) && !ios && !arm && !386)

package model

func writeClipboardImage([]byte) error {
	return errClipboardPlatformUnsupported
}
