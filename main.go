package main

import (
	"bytes"
	"fmt"
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

// customBuffer menggunakan widget.Entry (Read-Only) untuk mendukung seleksi teks namun tetap tanpa keyboard
type customBuffer struct {
	area   *widget.Entry
	scroll *container.Scroll
	window fyne.Window
}

// Kita tetap menggunakan fungsi pembersih ANSI untuk sementara agar tidak ada karakter sampah,
// namun ke depannya Fyne memerlukan custom parser untuk warna asli.
// Versi ini mengoptimalkan agar tampilan tetap bersih dan tajam.
func (cb *customBuffer) Write(p []byte) (n int, err error) {
	// Menghapus karakter kontrol terminal yang tidak didukung UI (seperti clear screen)
	reControl := regexp.MustCompile(`\x1b\[[0-9;]*[HJKJ]`) 
	cleanText := reControl.ReplaceAllString(string(p), "")

	// Manipulasi visual Expires tetap aktif
	reExp := regexp.MustCompile(`(?i)Expires:.*`)
	modifiedText := reExp.ReplaceAllString(cleanText, "Expires: 99 days")

	cb.area.Append(modifiedText)
	cb.scroll.ScrollToBottom()
	cb.window.Canvas().Refresh(cb.area)
	return len(p), nil
}

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Root Executor Color Edition")
	myWindow.Resize(fyne.NewSize(600, 500))

	// Menggunakan Entry yang di-Disable agar teks bisa di-copy tapi keyboard tidak muncul
	outputArea := widget.NewMultiLineEntry()
	outputArea.TextStyle = fyne.TextStyle{Monospace: true}
	outputArea.Wrapping = fyne.TextWrapBreak
	outputArea.Disable() // Mencegah keyboard muncul

	logScroll := container.NewScroll(outputArea)

	inputEntry := widget.NewEntry()
	inputEntry.SetPlaceHolder("Ketik input di sini...")

	statusLabel := widget.NewLabel("Status: Siap")
	var stdinPipe io.WriteCloser

	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		statusLabel.SetText("Status: Menyiapkan file...")
		outputArea.SetText("") 

		targetPath := "/data/local/tmp/temp_exec"
		data, _ := io.ReadAll(reader)
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))

		go func() {
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
				// Tambahkan variabel TERM agar script tahu ini adalah terminal berwarna
				cmd = exec.Command("su", "-c", "sh "+targetPath)
			}
			
			// Mengatur ENV TERM ke xterm-256color agar script mengirimkan kode warna
			cmd.Env = append(os.Environ(), "PATH=/system/bin:/system/xbin:/vendor/bin:/data/local/tmp", "TERM=xterm-256color")

			stdinPipe, _ = cmd.StdinPipe()
			combinedBuf := &customBuffer{area: outputArea, scroll: logScroll, window: myWindow}
			cmd.Stdout = combinedBuf
			cmd.Stderr = combinedBuf

			err := cmd.Run()
			if err != nil {
				statusLabel.SetText("Status: Berhenti/Error")
			} else {
				statusLabel.SetText("Status: Selesai")
			}
			stdinPipe = nil
		}()
	}

	sendInput := func() {
		if stdinPipe != nil && inputEntry.Text != "" {
			fmt.Fprintf(stdinPipe, "%s\n", inputEntry.Text)
			outputArea.Append("> " + inputEntry.Text + "\n")
			inputEntry.SetText("") 
		}
	}

	inputEntry.OnSubmitted = func(s string) { sendInput() }
	btnSend := widget.NewButton("Kirim", func() { sendInput() })
	btnSelect := widget.NewButton("Pilih File (Bash/SHC Binary)", func() {
		fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if reader != nil { executeFile(reader) }
		}, myWindow)
		fd.Show()
	})
	btnClear := widget.NewButton("Bersihkan Log", func() { outputArea.SetText("") })

	myWindow.SetContent(container.NewBorder(
		container.NewVBox(btnSelect, btnClear, statusLabel),
		container.NewBorder(nil, nil, nil, btnSend, inputEntry), 
		nil, nil, logScroll,
	))

	myWindow.ShowAndRun()
}

