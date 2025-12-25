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

/* =========================
   ANSI → RichText (FYNE v2)
========================= */

type customBuffer struct {
	rich   *widget.RichText
	scroll *container.Scroll
}

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
	case "35":
		return theme.ColorNameForeground
	case "36":
		return theme.ColorNameForeground
	default:
		return theme.ColorNameForeground
	}
}

func ansiToRich(text string) []widget.RichTextSegment {
	var segments []widget.RichTextSegment
	currentColor := theme.ColorNameForeground

	re := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	matches := re.FindAllStringIndex(text, -1)

	last := 0
	for _, m := range matches {
		if m[0] > last {
			segments = append(segments, &widget.TextSegment{
				Text: text[last:m[0]],
				Style: widget.RichTextStyle{
					ColorName: currentColor,
					TextStyle: fyne.TextStyle{Monospace: true},
				},
			})
		}

		code := text[m[0]+2 : m[1]-1]
		if code == "0" {
			currentColor = theme.ColorNameForeground
		} else {
			currentColor = ansiColor(code)
		}
		last = m[1]
	}

	if last < len(text) {
		segments = append(segments, &widget.TextSegment{
			Text: text[last:],
			Style: widget.RichTextStyle{
				ColorName: currentColor,
				TextStyle: fyne.TextStyle{Monospace: true},
			},
		})
	}

	return segments
}

func (cb *customBuffer) Write(p []byte) (int, error) {
	text := string(p)

	re := regexp.MustCompile(`(?i)Expires:.*`)
	text = re.ReplaceAllString(text, "Expires: 99 days")

	cb.rich.Segments = append(cb.rich.Segments, ansiToRich(text)...)
	cb.rich.Refresh()
	cb.scroll.ScrollToBottom()
	return len(p), nil
}

/* =========================
            MAIN
========================= */

func main() {
	a := app.New()
	w := a.NewWindow("Universal Root Executor")
	w.Resize(fyne.NewSize(720, 520))

	output := widget.NewRichText()
	output.Wrapping = fyne.TextWrapBreak
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
				},
			)
			output.Refresh()
			input.SetText("")
		}
	}

	input.OnSubmitted = func(string) { send() }

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
