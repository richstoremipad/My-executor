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
	case "1": // Bold sering dianggap putih/terang
		return theme.ColorNameForeground
	default:
		return theme.ColorNameForeground
	}
}

/* --- ANSI → RichText (SUPPORT MULTI CODE) --- */
func ansiToRich(text string) []widget.RichTextSegment {
	var segs []widget.RichTextSegment
	colorNow := theme.ColorNameForeground

	// Regex untuk menangkap kode warna ANSI: ESC[...m
	re := regexp.MustCompile(`\x1b\[([0-9;]+)m`)
	matches := re.FindAllStringSubmatchIndex(text, -1)

	last := 0
	for _, m := range matches {
		// Teks sebelum kode warna (menggunakan warna sebelumnya)
		if m[0] > last {
			segs = append(segs, &widget.TextSegment{
				Text: text[last:m[0]],
				Style: widget.RichTextStyle{
					ColorName: colorNow,
					TextStyle: fyne.TextStyle{Monospace: true},
				},
			})
		}

		// Parse kode warna (bisa multiple dipisah titik koma, misal 1;32)
		codes := strings.Split(text[m[2]:m[3]], ";")
		for _, c := range codes {
			if c == "0" {
				colorNow = theme.ColorNameForeground
			} else {
				// Update warna jika kode valid, jika tidak tetap warna terakhir
				if col := ansiColor(c); col != theme.ColorNameForeground {
					colorNow = col
				}
			}
		}

		last = m[1]
	}

	// Sisa teks setelah kode warna terakhir
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

/* --- WRITE OUTPUT (PERBAIKAN TAMPILAN) --- */
func (cb *customBuffer) Write(p []byte) (int, error) {
	raw := string(p)

	// --- [PERBAIKAN UTAMA DISINI] ---
	
	// 1. Ubah CRLF (\r\n) menjadi LF (\n) standar. 
	// Ini mencegah spasi ganda vertikal yang membuat tampilan renggang.
	raw = strings.ReplaceAll(raw, "\r\n", "\n")

	// 2. Hapus CR (\r) yang tersisa.
	// Script bash sering pakai \r untuk loading bar. Di log statis, 
	// \r bisa bikin tumpuk atau baris baru yang tidak perlu.
	raw = strings.ReplaceAll(raw, "\r", "")

	// 3. Hapus ANSI kontrol (bukan warna)
	raw = stripCursorANSI(raw)

	// Konversi ke format RichText Fyne
	segments := ansiToRich(raw)

	// Append ke output
	cb.rich.Segments = append(cb.rich.Segments, segments...)
	cb.rich.Refresh()
	
	// Scroll otomatis ke bawah
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

	/* OUTPUT */
	output := widget.NewRichText()
	output.Wrapping = fyne.TextWrapOff // PENTING: Agar ASCII Art tidak pecah ke bawah
	output.Scroll = container.ScrollNone
	
	scroll := container.NewScroll(output)

	/* INPUT */
	input := widget.NewEntry()
	input.SetPlaceHolder("Ketik input lalu Enter...")

	status := widget.NewLabel("Status: Siap")
	var stdin io.WriteCloser

	/* EXEC FILE */
	runFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()

		// Reset output
		output.Segments = nil
		output.Refresh()
		status.SetText("Status: Menyiapkan")

		data, _ := io.ReadAll(reader)
		target := "/data/local/tmp/temp_exec"
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))

		go func() {
			// Copy file ke /data/local/tmp dan beri izin eksekusi
			copyCmd := exec.Command("su", "-c", "cat > "+target+" && chmod 777 "+target)
			in, _ := copyCmd.StdinPipe()
			go func() {
				defer in.Close()
				in.Write(data)
			}()
			copyCmd.Run()

			status.SetText("Status: Berjalan")

			// Siapkan command eksekusi
			var cmd *exec.Cmd
			if isBinary {
				cmd = exec.Command("su", "-c", target)
			} else {
				// Gunakan sh untuk script shell agar kompatibilitas lebih baik
				cmd = exec.Command("su", "-c", "sh "+target)
			}

			// Hubungkan pipe
			stdin, _ = cmd.StdinPipe()
			buf := &customBuffer{rich: output, scroll: scroll}
			cmd.Stdout = buf
			cmd.Stderr = buf

			// Jalankan
			err := cmd.Run()
			if err != nil {
				buf.Write([]byte(fmt.Sprintf("\n\n[Process exited with error: %v]\n", err)))
			} else {
				buf.Write([]byte("\n\n[Process completed]\n"))
			}
			
			status.SetText("Status: Selesai")
			stdin = nil
		}()
	}

	/* SEND INPUT */
	send := func() {
		if stdin != nil && input.Text != "" {
			fmt.Fprintln(stdin, input.Text)
			
			// Tampilkan apa yang kita ketik ke log (warna biru biar beda)
			output.Segments = append(output.Segments, &widget.TextSegment{
				Text: "> " + input.Text + "\n",
				Style: widget.RichTextStyle{
					ColorName: theme.ColorNamePrimary,
					TextStyle: fyne.TextStyle{Monospace: true},
				},
			})
			output.Refresh()
			cbScroll := &customBuffer{rich: output, scroll: scroll}
			cbScroll.scroll.ScrollToBottom() // Scroll setelah input
			
			input.SetText("")
		}
	}
	input.OnSubmitted = func(string) { send() }

	/* UI LAYOUT */
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

