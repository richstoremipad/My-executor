package main

import (
	"bytes"
	"fmt"
	"image/color"
	"io"
	"os"
	"os/exec"
	"regexp"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

/* =========================
   ANSI → RichText SUPPORT
========================= */

type customBuffer struct {
	rich   *widget.RichText
	scroll *container.Scroll
	window fyne.Window
}

func ansiToRich(text string) []widget.RichTextSegment {
	segments := []widget.RichTextSegment{}
	currentColor := color.White

	re := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	matches := re.FindAllStringIndex(text, -1)

	last := 0
	for _, m := range matches {
		if m[0] > last {
			segments = append(segments, &widget.TextSegment{
				Text: text[last:m[0]],
				Style: widget.RichTextStyle{
					Color: currentColor,
				},
			})
		}

		code := text[m[0]+2 : m[1]-1]
		switch code {
		case "0":
			currentColor = color.White
		case "30":
			currentColor = color.Black
		case "31":
			currentColor = color.RGBA{255, 0, 0, 255}
		case "32":
			currentColor = color.RGBA{0, 255, 0, 255}
		case "33":
			currentColor = color.RGBA{255, 255, 0, 255}
		case "34":
			currentColor = color.RGBA{0, 128, 255, 255}
		case "35":
			currentColor = color.RGBA{255, 0, 255, 255}
		case "36":
			currentColor = color.RGBA{0, 255, 255, 255}
		case "37":
			currentColor = color.White
		}

		last = m[1]
	}

	if last < len(text) {
		segments = append(segments, &widget.TextSegment{
			Text: text[last:],
			Style: widget.RichTextStyle{
				Color: currentColor,
			},
		})
	}

	return segments
}

func (cb *customBuffer) Write(p []byte) (n int, err error) {
	text := string(p)

	// Modifikasi teks jika perlu
	re := regexp.MustCompile(`(?i)Expires:.*`)
	text = re.ReplaceAllString(text, "Expires: 99 days")

	segments := ansiToRich(text)
	cb.rich.Segments = append(cb.rich.Segments, segments...)
	cb.rich.Refresh()
	cb.scroll.ScrollToBottom()

	return len(p), nil
}

/* =========================
            MAIN
========================= */

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Universal Root Executor")
	myWindow.Resize(fyne.NewSize(700, 520))

	/* OUTPUT */
	outputRich := widget.NewRichText()
	outputRich.Wrapping = fyne.TextWrapBreak
	logScroll := container.NewScroll(outputRich)

	/* INPUT */
	inputEntry := widget.NewEntry()
	inputEntry.SetPlaceHolder("Ketik input lalu Enter...")

	statusLabel := widget.NewLabel("Status: Siap")
	var stdinPipe io.WriteCloser

	/* EXECUTOR */
	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()

		statusLabel.SetText("Status: Menyiapkan file...")
		outputRich.Segments = nil
		outputRich.Refresh()

		data, _ := io.ReadAll(reader)
		targetPath := "/data/local/tmp/temp_exec"

		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))

		go func() {
			// Copy file sebagai root
			copyCmd := exec.Command("su", "-c", "cat > "+targetPath+" && chmod 777 "+targetPath)
			copyStdin, _ := copyCmd.StdinPipe()
			go func() {
				defer copyStdin.Close()
				copyStdin.Write(data)
			}()
			copyCmd.Run()

			statusLabel.SetText("Status: Berjalan...")

			var cmd *exec.Cmd
			if isBinary {
				cmd = exec.Command("su", "-c", targetPath)
			} else {
				cmd = exec.Command("su", "-c", "sh "+targetPath)
			}

			cmd.Env = append(os.Environ(),
				"PATH=/system/bin:/system/xbin:/vendor/bin:/data/local/tmp",
				"TERM=xterm-256color",
			)

			stdinPipe, _ = cmd.StdinPipe()
			buffer := &customBuffer{
				rich:   outputRich,
				scroll: logScroll,
				window: myWindow,
			}

			cmd.Stdout = buffer
			cmd.Stderr = buffer

			err := cmd.Run()
			if err != nil {
				statusLabel.SetText("Status: Error")
				outputRich.Segments = append(outputRich.Segments,
					&widget.TextSegment{
						Text: "\n[ERROR] " + err.Error() + "\n",
						Style: widget.RichTextStyle{
							Color: color.RGBA{255, 0, 0, 255},
						},
					})
				outputRich.Refresh()
			} else {
				statusLabel.SetText("Status: Selesai")
			}
			stdinPipe = nil
		}()
	}

	/* INPUT SEND */
	sendInput := func() {
		if stdinPipe != nil && inputEntry.Text != "" {
			fmt.Fprintln(stdinPipe, inputEntry.Text)

			outputRich.Segments = append(outputRich.Segments,
				&widget.TextSegment{
					Text: "> " + inputEntry.Text + "\n",
					Style: widget.RichTextStyle{
						Color: color.RGBA{0, 255, 255, 255},
					},
				},
			)
			outputRich.Refresh()
			inputEntry.SetText("")
		}
	}

	inputEntry.OnSubmitted = func(_ string) { sendInput() }

	/* BUTTON */
	btnSend := widget.NewButton("Kirim", sendInput)
	btnClear := widget.NewButton("Bersihkan Log", func() {
		outputRich.Segments = nil
		outputRich.Refresh()
	})
	btnSelect := widget.NewButton("Pilih File (Shell / Binary)", func() {
		dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if reader != nil {
				executeFile(reader)
			}
		}, myWindow).Show()
	})

	/* LAYOUT */
	myWindow.SetContent(container.NewBorder(
		container.NewVBox(btnSelect, btnClear, statusLabel),
		container.NewBorder(nil, nil, nil, btnSend, inputEntry),
		nil, nil,
		logScroll,
	))

	myWindow.ShowAndRun()
}
