package contest

import (
	"encoding/json"
	"testing"
)

func TestNumStrAcceptsStringAndNumber(t *testing.T) {
	var v struct {
		A NumStr `json:"a"`
		B NumStr `json:"b"`
		C NumStr `json:"c"`
		D NumStr `json:"d"`
	}
	if err := json.Unmarshal([]byte(`{"a":"57000","b":381191140,"c":null,"d":12345678901234567890}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.A != "57000" || v.B != "381191140" || v.C != "" || v.D != "12345678901234567890" {
		t.Errorf("%+v", v)
	}
	if err := json.Unmarshal([]byte(`{"a":true}`), &v); err == nil {
		t.Error("불리언을 받아들였다")
	}
}
