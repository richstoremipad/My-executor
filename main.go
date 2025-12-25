package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// customBuffer untuk menangkap output dan memperbarui UI secara real-time
type customBuffer struct {
	area   *widget.Entry
	window fyne.Window
}

// Fungsi untuk menghapus kode warna ANSI (seperti [1;36m) agar teks bersih di UI
func cleanAnsi(str string) string {
	ansi := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	return ansi.ReplaceAllString(str, "")
}

func (cb *customBuffer) Write(p []byte) (n int, err error) {
	cleanText := cleanAnsi(string(p))
	cb.area.Append(cleanText)
	cb.window.Canvas().Refresh(cb.area)
	return len(p), nil
}

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Interactive Root Executor (SHC Ready)")
	myWindow.Resize(fyne.NewSize(600, 500))

	// Area Log Output
	outputArea := widget.NewMultiLineEntry()
	outputArea.TextStyle = fyne.TextStyle{Monospace: true}
	
	// Input field untuk user mengetik jawaban (seperti input tanggal)
	inputEntry := widget.NewEntry()
	inputEntry.SetPlaceHolder("Ketik input di sini lalu tekan Kirim/Enter...")

	statusLabel := widget.NewLabel("Status: Siap")
	
	var stdinPipe io.WriteCloser

	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		statusLabel.SetText("Status: Menyiapkan file...")
		outputArea.SetText("")

		// Simpan file ke folder internal aplikasi agar memiliki izin eksekusi
		internalPath := filepath.Join(myApp.Storage().RootURI().Path(), "temp_bin")
		out, _ := os.OpenFile(internalPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
		io.Copy(out, reader)
		out.Close()

		// Berikan izin eksekusi (chmod +x) agar file binary/SHC bisa jalan langsung
		os.Chmod(internalPath, 0755)

		go func() {
			statusLabel.SetText("Status: Berjalan (Interaktif)...")
			
			// Menjalankan file secara langsung (./) untuk mendukung SHC/Binary
			// Kita gunakan 'cd' ke folder internal untuk memastikan path benar
			cmdString := fmt.Sprintf("cd %s && ./%s", filepath.Dir(internalPath), filepath.Base(internalPath))
			cmd := exec.Command("su", "-c", cmdString)
			
			// Setup Environment agar perintah dasar ditemukan
			cmd.Env = append(os.Environ(), "PATH=/system/bin:/system/xbin:/vendor/bin", "TERM=dumb")

			// Hubungkan Stdin agar bisa menerima input dari UI
			stdinPipe, _ = cmd.StdinPipe()
			
			combinedBuf := &customBuffer{area: outputArea, window: myWindow}
			cmd.Stdout = combinedBuf
			cmd.Stderr = combinedBuf

			err := cmd.Run()
			
			if err != nil {
				statusLabel.SetText("Status: Berhenti/Error")
				outputArea.Append("\n[Proses Keluar: " + err.Error() + "]\n")
			} else {
				statusLabel.SetText("Status: Selesai")
				outputArea.Append("\n[Proses Selesai]\n")
			}
			stdinPipe = nil
		}()
	}

	// Fungsi mengirim input ke proses yang sedang berjalan
	sendInput := func() {
		if stdinPipe != nil && inputEntry.Text != "" {
			fmt.Fprintf(stdinPipe, "%s\n", inputEntry.Text)
			outputArea.Append(fmt.Sprintf("> %s\n", inputEntry.Text)) 
			inputEntry.SetText("") 
		}
	}

	inputEntry.OnSubmitted = func(s string) { sendInput() }
	btnSend := widget.NewButton("Kirim", func() { sendInput() })

	btnSelect := widget.NewButton("Pilih File (Bash/SHC)", func() {
		fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if reader != nil { executeFile(reader) }
		}, myWindow)
		fd.Show()
	})

	btnClear := widget.NewButton("Bersihkan Log", func() {
		outputArea.SetText("")
	})

	// Layouting
	inputContainer := container.NewBorder(nil, nil, nil, btnSend, inputEntry)
	controls := container.NewVBox(btnSelect, btnClear, statusLabel)

	myWindow.SetContent(container.NewBorder(
		controls,
		inputContainer, 
		nil, nil, 
		outputArea,
	))

	myWindow.ShowAndRun()
}

