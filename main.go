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
	// Menghapus kode ANSI non-warna (seperti clear screen, cursor movement)
	// Ini penting agar log tidak berantakan
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
					TextStyle: fyne.TextStyle{Monospace: true}, // Wajib Monospace agar ASCII lurus
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

/* --- WRITE OUTPUT --- */
func (cb *customBuffer) Write(p []byte) (int, error) {
	raw := string(p)

	// 1. Ubah CRLF (\r\n) menjadi \n (Standar Unix)
	// Ini menghilangkan spasi ganda akibat format Windows/Network
	raw = strings.ReplaceAll(raw, "\r\n", "\n")

	// 2. Hapus CR (\r) yang berdiri sendiri.
	// Jika tidak dihapus, loading bar bisa membuat baris baru terus menerus.
	raw = strings.ReplaceAll(raw, "\r", "")

	// CATATAN: Saya menghapus pembersihan "\n\n" yang ada di revisi sebelumnya
	// karena itu yang menyebabkan ASCII Art "XFILES" menjadi rusak/penyet.

	// 3. Hapus ANSI cursor movement
	raw = stripCursorANSI(raw)

	// Jangan proses jika kosong
	if raw == "" {
		return len(p), nil
	}

	segments := ansiToRich(raw)
	
	// Gunakan Refresh di main thread
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
	output.Wrapping = fyne.TextWrapOff 
	output.Scroll = container.ScrollNone // Kita handle scroll sendiri via container
	
	scroll := container.NewScroll(output)

	/* INPUT */
	input := widget.NewEntry()
	input.SetPlaceHolder("Ketik input lalu Enter...")

	status := widget.NewLabel("Status: Siap")
	var stdin io.WriteCloser

	/* FUNCTION RUN FILE */
	runFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()

		output.Segments = nil
		output.Refresh()
		status.SetText("Status: Menyiapkan")

		data, _ := io.ReadAll(reader)
		target := "/data/local/tmp/temp_exec"
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))

		go func() {
			// Copy file
			copyCmd := exec.Command("su", "-c", "cat > "+target+" && chmod 777 "+target)
			in, _ := copyCmd.StdinPipe()
			go func() {
				defer in.Close()
				in.Write(data)
			}()
			copyCmd.Run()

			status.SetText("Status: Berjalan")

			var cmd *exec.Cmd
			// Gunakan 'sh' untuk script shell agar lebih kompatibel
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
			
			if err != nil {
				buf.Write([]byte(fmt.Sprintf("\n[EXIT ERROR: %v]\n", err)))
			} else {
				buf.Write([]byte("\n[Selesai]\n"))
			}
			
			status.SetText("Status: Selesai")
			stdin = nil
		}()
	}

	/* SEND INPUT */
	send := func() {
		if stdin != nil && input.Text != "" {
			fmt.Fprintln(stdin, input.Text)
			
			output.Segments = append(output.Segments, &widget.TextSegment{
				Text: "> " + input.Text + "\n",
				Style: widget.RichTextStyle{
					ColorName: theme.ColorNamePrimary,
					TextStyle: fyne.TextStyle{Monospace: true},
				},
			})
			output.Refresh()
			
			// Scroll ke bawah manual setelah kirim
			cbScroll := &customBuffer{rich: output, scroll: scroll}
			cbScroll.scroll.ScrollToBottom() 
			
			input.SetText("")
		}
	}
	input.OnSubmitted = func(string) { send() }

	/* UI LAYOUT - KEMBALI KE ORIGINAL */
	// Menggunakan Layout VBox untuk tombol di atas (seperti request awal)
	topContainer := container.NewVBox(
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
	)

	bottomContainer := container.NewBorder(nil, nil, nil, widget.NewButton("Kirim", send), input)

	w.SetContent(container.NewBorder(
		topContainer,    // Tombol di Atas (Vertikal Stack)
		bottomContainer, // Input di Bawah
		nil, nil,
		scroll,          // Output di Tengah
	))

	w.ShowAndRun()
}

