package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Executor ARM64")
	myWindow.Resize(fyne.NewSize(600, 400))

	outputArea := widget.NewMultiLineEntry()
	statusLabel := widget.NewLabel("Status: Ready")

	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		statusLabel.SetText("Status: Menyiapkan file...")

		// 1. Buat path di folder internal aplikasi
		// Ini adalah area 'Sandbox' yang diizinkan untuk eksekusi
		tmpPath := filepath.Join(myApp.Storage().RootURI().Path(), "run_script.sh")

		// 2. Salin isi file dari picker ke folder internal
		out, err := os.OpenFile(tmpPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
		if err != nil {
			outputArea.SetText("Gagal membuat file internal: " + err.Error())
			return
		}
		_, err = io.Copy(out, reader)
		out.Close()

		if err != nil {
			outputArea.SetText("Gagal menyalin file: " + err.Error())
			return
		}

		// 3. Eksekusi
		statusLabel.SetText("Status: Menjalankan...")
		cmd := exec.Command("sh", tmpPath)
		output, err := cmd.CombinedOutput()

		if err != nil {
			outputArea.SetText(fmt.Sprintf("Error: %v\n\nLog:\n%s", err, string(output)))
			statusLabel.SetText("Status: Gagal")
		} else {
			outputArea.SetText(string(output))
			statusLabel.SetText("Status: Sukses")
		}
	}

	btnSelect := widget.NewButton("Pilih dan Jalankan Script", func() {
		fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if err == nil && reader != nil {
				executeFile(reader)
			}
		}, myWindow)
		fd.Show()
	})

	myWindow.SetContent(container.NewBorder(
		container.NewVBox(btnSelect, statusLabel),
		nil, nil, nil, outputArea,
	))
	myWindow.ShowAndRun()
}

