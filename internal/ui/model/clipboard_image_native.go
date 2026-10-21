//go:build (darwin || windows) && !arm && !386 && !ios

package model

import "github.com/aymanbagabas/go-nativeclipboard"

func writeClipboardImage(png []byte) error {
	_, err := nativeclipboard.Image.Write(png)
	return err
}
