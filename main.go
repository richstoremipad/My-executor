package main

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

/* ===============================
        TERMINAL BUFFER
================================ */

type customBuffer struct {
	rich     *widget.RichText
	scroll   *container.Scroll
	lastLine []widget.RichTextSegment
	mu       sync.Mutex
}

/* --- Hapus ANSI selain warna --- */
func stripCursorANSI(s string) string {
	re := regexp.MustCompile(`\x1b\[[0-9;]*[A-HJKSTf]`)
	return re.ReplaceAllString(s, "")
}

func ansiColor(code string) fyne.ThemeColorName {
	switch code {
	case "31":
		return theme.ColorNameError
	case "32":
		return theme.ColorNameSuccess
	case "33":
		return theme.ColorNameWarning
	case "34", "36":
		return theme.ColorNamePrimary
	default:
		return theme.ColorNameForeground
	}
}

func ansiToRich(text string) []widget.RichTextSegment {
	var segs []widget.RichTextSegment
	colorNow := theme.ColorNameForeground

	re := regexp.MustCompile(`\x1b\[([0-9;]+)m`)
	matches := re.FindAllStringSubmatchIndex(text, -1)

	last := 0
	for _, m := range matches {
		if m[0] > last {
			segs = append(segs, &widget.TextSegment{
				Text: text[last:m[0]],
				Style: widget.RichTextStyle{
					ColorName: colorNow,
					TextStyle: fyne.TextStyle{Monospace: true},
				},
			})
		}

		codes := strings.Split(text[m[2]:m[3]], ";")
		colorNow = theme.ColorNameForeground
		for _, c := range codes {
			if c != "0" {
				colorNow = ansiColor(c)
			}
		}
		last = m[1]
	}

	if last < len(text) {
		segs = append(segs, &widget.TextSegment{
			Text: text[last:],
			Style: widget.RichTextStyle{
				ColorName: colorNow,
				TextStyle: fyne.TextStyle{Monospace: true},
			},
		})
	}

	return segs
}

/* --- TERMINAL WRITE (FIX \r) --- */
func (cb *customBuffer) Write(p []byte) (int, error) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	raw := stripCursorANSI(string(p))

	parts := strings.Split(raw, "\r")

	for i, part := range parts {
		segs := ansiToRich(part)

		if i == 0 && !strings.HasPrefix(raw, "\r") {
			cb.rich.Segments = append(cb.rich.Segments, segs...)
		} else {
			// overwrite last line
			if len(cb.rich.Segments) > 0 {
				cb.rich.Segments = cb.rich.Segments[:len(cb.rich.Segments)-len(cb.lastLine)]
			}
			cb.rich.Segments = append(cb.rich.Segments, segs...)
		}

		cb.lastLine = segs
	}

	cb.rich.Refresh()
	cb.scroll.ScrollToBottom()
	return len(p), nil
}

/* ===============================
              MAIN
================================ */

func main() {
	a := app.New()
	a.Settings().SetTheme(theme.DarkTheme())

	w := a.NewWindow("Universal Root Executor")
	w.Resize(fyne.NewSize(740, 540))

	output := widget.NewRichText()
	output.Wrapping = fyne.TextWrapOff
	scroll := container.NewScroll(output)

	input := widget.NewEntry()
	input.SetPlaceHolder("Ketik input lalu Enter...")
	status := widget.NewLabel("Status: Siap")

	var stdin io.WriteCloser

	runFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()

		output.Segments = nil
		output.Refresh()
		status.SetText("Status: Menyiapkan")

		data, _ := io.ReadAll(reader)
		target := "/data/local/tmp/temp_exec"
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))

		go func() {
			exec.Command("su", "-c", "cat > "+target+" && chmod 777 "+target).Run()

			cmd := exec.Command("su", "-c", func() string {
				if isBinary {
					return target
				}
				return "sh " + target
			}())

			stdin, _ = cmd.StdinPipe()
			buf := &customBuffer{rich: output, scroll: scroll}
			cmd.Stdout = buf
			cmd.Stderr = buf

			status.SetText("Status: Berjalan")
			cmd.Run()
			status.SetText("Status: Selesai")
			stdin = nil
		}()
	}

	send := func() {
		if stdin != nil && input.Text != "" {
			fmt.Fprintln(stdin, input.Text)
			input.SetText("")
		}
	}
	input.OnSubmitted = func(string) { send() }

	top := container.NewHBox(
		widget.NewButton("Pilih File", func() {
			dialog.NewFileOpen(func(r fyne.URIReadCloser, _ error) {
				if r != nil {
					runFile(r)
				}
			}, w).Show()
		}),
		widget.NewButton("Clear Log", func() {
			output.Segments = nil
			output.Refresh()
		}),
		layout.NewSpacer(),
		status,
	)

	w.SetContent(container.NewBorder(
		top,
		container.NewBorder(nil, nil, nil, widget.NewButton("Kirim", send), input),
		nil, nil,
		scroll,
	))

	w.ShowAndRun()
}
