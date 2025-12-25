package main

import (
	"fmt"
	"os/exec"
	"runtime"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Executor SH/Bin")
	myWindow.Resize(fyne.NewSize(600, 400))

	outputArea := widget.NewMultiLineEntry()
	outputArea.SetPlaceHolder("Output log akan muncul di sini...")
	
	statusLabel := widget.NewLabel("Status: Ready")

	executeFile := func(path string) {
		statusLabel.SetText("Status: Menjalankan " + path)
		
		var cmd *exec.Cmd
		// Cek OS, jika Windows butuh penanganan berbeda, 
		// tapi asumsikan Linux/macOS untuk .sh dan bin
		if runtime.GOOS == "windows" {
			cmd = exec.Command("cmd", "/C", path)
		} else {
			// Memberikan izin eksekusi (chmod +x) secara otomatis
			exec.Command("chmod", "+x", path).Run()
			cmd = exec.Command(path)
		}

		output, err := cmd.CombinedOutput()
		if err != nil {
			outputArea.SetText(fmt.Sprintf("Error: %v\n\nOutput:\n%s", err, string(output)))
			statusLabel.SetText("Status: Gagal")
			return
		}

		outputArea.SetText(string(output))
		statusLabel.SetText("Status: Selesai")
	}

	btnSelect := widget.NewButton("Pilih dan Jalankan File", func() {
		fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if err != nil || reader == nil {
				return
			}
			executeFile(reader.URI().Path())
		}, myWindow)
		fd.Show()
	})

	content := container.NewBorder(
		container.NewVBox(btnSelect, statusLabel), 
		nil, nil, nil, 
		outputArea,
	)

	myWindow.SetContent(content)
	myWindow.ShowAndRun()
}

