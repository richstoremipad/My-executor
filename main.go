package main

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

/* ===============================
          BUFFER OUTPUT
================================ */

type customBuffer struct {
	rich   *widget.RichText
	scroll *container.Scroll
}

/* --- HAPUS ANSI KONTROL (CURSOR, CLEAR) --- */
func stripCursorANSI(s string) string {
	re := regexp.MustCompile(`\x1b\[[0-9;]*[A-HJKSTf]`)
	return re.ReplaceAllString(s, "")
}

/* --- MAP ANSI COLOR → FYNE --- */
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
	case "36":
		return theme.ColorNamePrimary
	case "1": // Bold
		return theme.ColorNameForeground
	default:
		return theme.ColorNameForeground
	}
}

/* --- ANSI → RichText --- */
func ansiToRich(text string) []widget.RichTextSegment {
	var segs []widget.RichTextSegment
	colorNow := theme.ColorNameForeground

	// Regex ANSI Color
	re := regexp.MustCompile(`\x1b\[([0-9;]+)m`)
	matches := re.FindAllStringSubmatchIndex(text, -1)

	last := 0
	for _, m := range matches {
		if m[0] > last {
			segs = append(segs, &widget.TextSegment{
				Text: text[last:m[0]],
				Style: widget.RichTextStyle{
					ColorName: colorNow,
					TextStyle: fyne.TextStyle{Monospace: true}, // Wajib Monospace
				},
			})
		}

		codes := strings.Split(text[m[2]:m[3]], ";")
		for _, c := range codes {
			if c == "0" {
				colorNow = theme.ColorNameForeground
			} else {
				if col := ansiColor(c); col != theme.ColorNameForeground {
					colorNow = col
				}
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

/* --- WRITE OUTPUT (FIX RAPAT / COMPACT) --- */
func (cb *customBuffer) Write(p []byte) (int, error) {
	raw := string(p)

	// 1. Normalisasi CRLF ke LF
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	
	// 2. Hapus CR sisa
	raw = strings.ReplaceAll(raw, "\r", "")

	// 3. [FIX UTAMA] Hapus Double Newline (\n\n jadi \n)
	// Ulangi terus sampai tidak ada spasi ganda vertikal
	for strings.Contains(raw, "\n\n") {
		raw = strings.ReplaceAll(raw, "\n\n", "\n")
	}

	// 4. Bersihkan kode kursor
	raw = stripCursorANSI(raw)

	// Jika string kosong setelah dibersihkan, jangan append segment kosong
	if raw == "" {
		return len(p), nil
	}

	segments := ansiToRich(raw)
	
	// Update UI di Main Thread (agar aman)
	cb.rich.Segments = append(cb.rich.Segments, segments...)
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
	w.Resize(fyne.NewSize(720, 520))

	/* OUTPUT CONFIG */
	output := widget.NewRichText()
	output.Wrapping = fyne.TextWrapOff // Mencegah teks turun otomatis jika layar sempit
	output.Scroll = container.ScrollNone
	
	scroll := container.NewScroll(output)

	/* INPUT */
	input := widget.NewEntry()
	input.SetPlaceHolder("Ketik perintah/pilihan menu...")

	status := widget.NewLabel("Status: Siap")
	status.TextStyle = fyne.TextStyle{Bold: true}
	
	var stdin io.WriteCloser

	/* FUNCTION RUN FILE */
	runFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()

		output.Segments = nil
		output.Refresh()
		status.SetText("Status: Menyiapkan...")

		data, _ := io.ReadAll(reader)
		target := "/data/local/tmp/temp_exec"
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))

		go func() {
			// Copy file dan chmod
			copyCmd := exec.Command("su", "-c", "cat > "+target+" && chmod 777 "+target)
			in, _ := copyCmd.StdinPipe()
			go func() {
				defer in.Close()
				in.Write(data)
			}()
			copyCmd.Run()

			status.SetText("Status: Berjalan")

			var cmd *exec.Cmd
			// Gunakan 'sh' agar environment variabel lebih stabil
			if isBinary {
				cmd = exec.Command("su", "-c", target)
			} else {
				cmd = exec.Command("su", "-c", "sh "+target)
			}

			stdin, _ = cmd.StdinPipe()
			buf := &customBuffer{rich: output, scroll: scroll}
			cmd.Stdout = buf
			cmd.Stderr = buf

			err := cmd.Run()
			
			// Tampilkan status akhir
			if err != nil {
				buf.Write([]byte(fmt.Sprintf("\n[EXIT: %v]", err)))
			} else {
				buf.Write([]byte("\n[Selesai]"))
			}
			status.SetText("Status: Selesai")
			stdin = nil
		}()
	}

	/* SEND INPUT */
	send := func() {
		if stdin != nil && input.Text != "" {
			fmt.Fprintln(stdin, input.Text)
			
			// Tampilkan input user dengan warna biru
			output.Segments = append(output.Segments, &widget.TextSegment{
				Text: "> " + input.Text + "\n",
				Style: widget.RichTextStyle{
					ColorName: theme.ColorNamePrimary,
					TextStyle: fyne.TextStyle{Monospace: true},
				},
			})
			output.Refresh()
			
			// Paksa scroll ke bawah
			cbScroll := &customBuffer{rich: output, scroll: scroll}
			cbScroll.scroll.ScrollToBottom()
			
			input.SetText("")
		}
	}
	input.OnSubmitted = func(string) { send() }

	/* LAYOUT */
	// Toolbar atas
	topBar := container.NewVBox(
		container.NewHBox(
			widget.NewButtonWithIcon("Pilih File", theme.FileIcon(), func() {
				dialog.NewFileOpen(func(r fyne.URIReadCloser, _ error) {
					if r != nil {
						runFile(r)
					}
				}, w).Show()
			}),
			widget.NewButtonWithIcon("Clear Log", theme.ContentClearIcon(), func() {
				output.Segments = nil
				output.Refresh()
			}),
		),
		status,
		widget.NewSeparator(),
	)

	// Input area bawah
	bottomBar := container.NewBorder(nil, nil, nil, 
		widget.NewButtonWithIcon("Kirim", theme.ConfirmIcon(), send), 
		input,
	)

	w.SetContent(container.NewBorder(
		topBar,
		bottomBar,
		nil, nil,
		scroll,
	))

	w.ShowAndRun()
}
