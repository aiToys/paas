package apiroute

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

func TestRawMessageIsJSONValue(t *testing.T) {
	r := New(http.NewServeMux(), Info{})
	s := r.schemaOf(reflect.TypeOf(json.RawMessage{}))
	if s.Type != "" || s.Items != nil {
		t.Fatalf("RawMessage 不应声明为字节数组: %+v", s)
	}
}
