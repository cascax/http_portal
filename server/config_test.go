package main

import "testing"

func TestReadConfig(t *testing.T) {
	c, err := ReadConfig("./portal_server.yml")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(c)
}
