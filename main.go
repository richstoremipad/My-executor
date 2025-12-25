package main

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"regexp"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

/* ===============================
   BUFFER OUTPUT (FIXED)
================================ */

type customBuffer struct {
	rich   *widget.RichText
	scroll *container.Scroll
}

/* --- Hapus ANSI CONTROL (cursor, clear, dll) --- */
func stripControlANSI(s string) string {
	re := regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	return re.ReplaceAllString(s, "")
}

/* --- Map ANSI color → Fyne Theme --- */
func ansiColor(code string) fyne.ThemeColorName {
	switch code {
	case "31":
		return theme.ColorNameError
	case "32":
		return theme.ColorNameSuccess
	case "33":
		return theme.ColorNameWarning
	case "34":
		return theme.ColorNamePrimary
	default:
		return theme.ColorNameForeground
	}
}

/* --- ANSI → RichText (COLOR ONLY) --- */
func ansiToRich(text string) []widget.RichTextSegment {
	var segs []widget.RichTextSegment
	colorNow := theme.ColorNameForeground

	re := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	matches := re.FindAllStringIndex(text, -1)

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

		code := text[m[0]+2 : m[1]-1]
		if code == "0" {
			colorNow = theme.ColorNameForeground
		} else {
			colorNow = ansiColor(code)
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

/* --- WRITE OUTPUT --- */
func (cb *customBuffer) Write(p []byte) (int, error) {
	text := stripControlANSI(string(p))

	cb.rich.Segments = append(cb.rich.Segments, ansiToRich(text)...)
	cb.rich.Refresh()
	cb.scroll.ScrollToBottom()

	return len(p), nil
}

/* ===============================
              MAIN
================================ */

func main() {
	a := app.New()
	w := a.NewWindow("Universal Root Executor")
	w.Resize(fyne.NewSize(720, 520))

	/* OUTPUT */
	output := widget.NewRichText()
	output.Wrapping = fyne.TextWrapOff // <<< PENTING (BIAR ASCII TIDAK PECAH)
	scroll := container.NewScroll(output)

	/* INPUT */
	input := widget.NewEntry()
	input.SetPlaceHolder("Ketik input lalu Enter...")

	status := widget.NewLabel("Status: Siap")
	var stdin io.WriteCloser

	/* EXEC FILE */
	runFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()

		output.Segments = nil
		output.Refresh()
		status.SetText("Status: Menyiapkan")

		data, _ := io.ReadAll(reader)
		target := "/data/local/tmp/temp_exec"
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))

		go func() {
			copyCmd := exec.Command("su", "-c", "cat > "+target+" && chmod 777 "+target)
			in, _ := copyCmd.StdinPipe()
			go func() {
				defer in.Close()
				in.Write(data)
			}()
			copyCmd.Run()

			status.SetText("Status: Berjalan")

			var cmd *exec.Cmd
			if isBinary {
				cmd = exec.Command("su", "-c", target)
			} else {
				cmd = exec.Command("su", "-c", "sh "+target)
			}

			stdin, _ = cmd.StdinPipe()
			buf := &customBuffer{rich: output, scroll: scroll}
			cmd.Stdout = buf
			cmd.Stderr = buf

			cmd.Run()
			status.SetText("Status: Selesai")
			stdin = nil
		}()
	}

	/* SEND INPUT */
	send := func() {
		if stdin != nil && input.Text != "" {
			fmt.Fprintln(stdin, input.Text)
			output.Segments = append(output.Segments,
				&widget.TextSegment{
					Text: "> " + input.Text + "\n",
					Style: widget.RichTextStyle{
						ColorName: theme.ColorNamePrimary,
						TextStyle: fyne.TextStyle{Monospace: true},
					},
				})
			output.Refresh()
			input.SetText("")
		}
	}
	input.OnSubmitted = func(string) { send() }

	/* UI */
	w.SetContent(container.NewBorder(
		container.NewVBox(
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
			status,
		),
		container.NewBorder(nil, nil, nil, widget.NewButton("Kirim", send), input),
		nil, nil,
		scroll,
	))

	w.ShowAndRun()
}
