package model

import "errors"

var errNoImageClipboardTool = errors.New("copying images needs wl-copy (wl-clipboard) or xclip")

// imageCopyCommand is the command that puts a PNG read from stdin on the
// Linux clipboard: wl-copy under Wayland, otherwise xclip. They stay
// behind to serve the image, and do large transfers properly, which the
// native X11 clipboard in go-nativeclipboard doesn't.
func imageCopyCommand(getenv func(string) string, lookPath func(string) (string, error)) ([]string, error) {
	if getenv("WAYLAND_DISPLAY") != "" {
		if _, err := lookPath("wl-copy"); err == nil {
			return []string{"wl-copy", "--type", "image/png"}, nil
		}
	}
	if _, err := lookPath("xclip"); err == nil {
		return []string{"xclip", "-selection", "clipboard", "-t", "image/png", "-i"}, nil
	}
	return nil, errNoImageClipboardTool
}
