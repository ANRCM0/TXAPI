package jsonutil

import jsonvalue "minigateway/internal/foundation/contracts/json/v1"

func Validate(body []byte) error { return jsonvalue.Validate(body) }
