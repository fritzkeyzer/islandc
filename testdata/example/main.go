package main

import (
	"os"

	"github.com/fritzkeyzer/islandc/testdata"
)

func main() {
	w, err := os.Create("testdata/color_picker.rendered.html")
	if err != nil {
		panic(err)
	}
	defer w.Close()

	err = testdata.RenderColorPicker(w, testdata.ColorPickerData{
		H: 220,
		S: 80,
		L: 55,
		Presets: []testdata.ColorPickerDataPresets{
			{Name: "ocean", H: 210, S: 80, L: 55},
			{Name: "sunset", H: 18, S: 90, L: 58},
		},
	})
	if err != nil {
		panic(err)
	}
}
