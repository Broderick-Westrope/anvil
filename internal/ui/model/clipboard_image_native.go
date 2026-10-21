//go:build (darwin || windows) && !ios && !arm && !386

package model

import "github.com/aymanbagabas/go-nativeclipboard"

func writeClipboardImage(png []byte) error {
	_, err := nativeclipboard.Image.Write(png)
	return err
}

// imageClipboardReady is nil: the native clipboard is always there.
func imageClipboardReady() error { return nil }
