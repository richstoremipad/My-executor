package main

import (
	"bytes"
	"fmt"
	"image/color"
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
      CUSTOM THEME (AGAR RAPAT)
================================ */

// myTheme digunakan untuk memaksa jarak antar baris (Padding) menjadi 0
// dan mengecilkan font sedikit agar mirip terminal MT Manager.
type myTheme struct{}

func (m myTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	return theme.DefaultTheme().Color(name, variant)
}

func (m myTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(name)
}

func (m myTheme) Font(style fyne.TextStyle) fyne.Resource {
	// Pastikan font Monospace digunakan
	if style.Monospace {
		return theme.DefaultTheme().Font(style)
	}
	return theme.DefaultTheme().Font(style)
}

func (m myTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNamePadding:
		return 0 // PENTING: Menghilangkan jarak antar elemen/baris
	case theme.SizeNameText:
		return 12 // Ukuran font sedikit lebih kecil agar muat banyak (Compact)
	case theme.SizeNameInnerPadding:
		return 2
	default:
		return theme.DefaultTheme().Size(name)
	}
}

var _ fyne.Theme = (*myTheme)(nil)

/* ===============================
          BUFFER OUTPUT
================================ */

type customBuffer struct {
	rich   *widget.RichText
	scroll *container.Scroll
}

/* --- HAPUS ANSI KONTROL --- */
func stripCursorANSI(s string) string {
	re := regexp.MustCompile(`\x1b\[[0-9;]*[A-HJKSTf]`)
	return re.ReplaceAllString(s, "")
}

/* --- MAP ANSI COLOR --- */
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
	case "1":
		return theme.ColorNameForeground
	default:
		return theme.ColorNameForeground
	}
}

/* --- ANSI → RichText --- */
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

/* --- WRITE OUTPUT (LOGIKA OVERWRITE) --- */
func (cb *customBuffer) Write(p []byte) (int, error) {
	raw := string(p)

	// 1. Ubah CRLF jadi LF biasa
	raw = strings.ReplaceAll(raw, "\r\n", "\n")

	// 2. [LOGIKA PENTING] Penanganan \r (Carriage Return)
	// Script bash sering output: "Loading..." lalu "\rSelesai".
	// Kita harus memecah berdasarkan baris (\n), lalu di setiap baris
	// kita pecah berdasarkan \r dan hanya mengambil bagian TERAKHIR.
	// Ini membuat tampilan jadi satu baris (seperti "✓ Checking...").
	
	var cleanLines []string
	lines := strings.Split(raw, "\n")
	
	for _, line := range lines {
		if strings.Contains(line, "\r") {
			parts := strings.Split(line, "\r")
			// Ambil bagian terakhir saja (simulasi overwrite)
			cleanLines = append(cleanLines, parts[len(parts)-1])
		} else {
			cleanLines = append(cleanLines, line)
		}
	}
	// Gabungkan kembali
	raw = strings.Join(cleanLines, "\n")

	// 3. Hapus ANSI cursor movement
	raw = stripCursorANSI(raw)

	if raw == "" {
		return len(p), nil
	}

	// 4. Konversi ke Segment
	newSegments := ansiToRich(raw)

	// Optimasi penggabungan text segment (biar makin rapat)
	if len(cb.rich.Segments) > 0 {
		lastIdx := len(cb.rich.Segments) - 1
		if lastSeg, ok := cb.rich.Segments[lastIdx].(*widget.TextSegment); ok {
			if len(newSegments) > 0 {
				if firstNewSeg, ok2 := newSegments[0].(*widget.TextSegment); ok2 {
					if lastSeg.Style == firstNewSeg.Style {
						lastSeg.Text += firstNewSeg.Text
						newSegments = newSegments[1:]
					}
				}
			}
		}
	}

	cb.rich.Segments = append(cb.rich.Segments, newSegments...)
	cb.rich.Refresh()
	cb.scroll.ScrollToBottom()

	return len(p), nil
}

/* ===============================
              MAIN
================================ */

func main() {
	a := app.New()
	
	// SET THEME CUSTOM KITA DI SINI
	a.Settings().SetTheme(&myTheme{})

	w := a.NewWindow("Universal Root Executor")
	w.Resize(fyne.NewSize(720, 520))

	/* OUTPUT CONFIG */
	output := widget.NewRichText()
	output.Wrapping = fyne.TextWrapOff 
	output.Scroll = container.ScrollNone
	
	scroll := container.NewScroll(output)

	/* INPUT */
	input := widget.NewEntry()
	input.SetPlaceHolder("Ketik perintah...")

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
			copyCmd := exec.Command("su", "-c", "cat > "+target+" && chmod 777 "+target)
			in, _ := copyCmd.StdinPipe()
			go func() {
				defer in.Close()
				in.Write(data)
			}()
			copyCmd.Run()

			status.SetText("Status: Berjalan")

			var cmd *exec.Cmd
			// Tambahkan stty -echo agar input tidak double, dan sh
			cmdString := fmt.Sprintf("stty -echo; sh %s", target)
			if isBinary {
				cmdString = target
			}
			
			cmd = exec.Command("su", "-c", cmdString)

			stdin, _ = cmd.StdinPipe()
			buf := &customBuffer{rich: output, scroll: scroll}
			cmd.Stdout = buf
			cmd.Stderr = buf

			err := cmd.Run()
			
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
			
			output.Segments = append(output.Segments, &widget.TextSegment{
				Text: "> " + input.Text + "\n",
				Style: widget.RichTextStyle{
					ColorName: theme.ColorNamePrimary,
					TextStyle: fyne.TextStyle{Monospace: true},
				},
			})
			output.Refresh()
			
			cbScroll := &customBuffer{rich: output, scroll: scroll}
			cbScroll.scroll.ScrollToBottom()
			
			input.SetText("")
		}
	}
	input.OnSubmitted = func(string) { send() }

	/* UI LAYOUT (VERTIKAL TOMBOL DI ATAS) */
	topControl := container.NewVBox(
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

	bottomControl := container.NewBorder(nil, nil, nil, widget.NewButton("Kirim", send), input)

	w.SetContent(container.NewBorder(
		topControl,    
		bottomControl, 
		nil, nil,      
		scroll,        
	))

	w.ShowAndRun()
}

