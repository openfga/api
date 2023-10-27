package main

import (
	"fmt"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func main() {

	json := `{"source_info": {"line": 1, "column": 2}}`

	sourceInfo := &openfgav1.SourceLocationInfo{}

	err := protojson.Unmarshal([]byte(json), sourceInfo)
	if err != nil {
		panic(err)
	}

	fmt.Printf("%v\n", sourceInfo)
}
