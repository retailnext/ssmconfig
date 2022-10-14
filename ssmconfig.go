// Copyright 2022 RetailNext, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ssmconfig

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

type Request interface {
	Send(ctx context.Context) error
}

type MissingParameters []string

func (e MissingParameters) Error() string {
	return fmt.Sprintf("missing ssm parameters: %+v", []string(e))
}

func NewRequest(configurable interface{}, path string, client ssm.GetParametersByPathAPIClient) Request {
	path = "/" + strings.Trim(path, "/")

	input := ssm.GetParametersByPathInput{
		Path:           &path,
		WithDecryption: aws.Bool(true),
	}

	v := reflect.ValueOf(configurable)
	if v.Kind() != reflect.Ptr {
		panic("configurable must be a pointer")
	}
	v = v.Elem()

	r := request{
		missing:   make(map[string]struct{}, v.NumField()),
		setters:   make(map[string][]func(string), v.NumField()),
		paginator: ssm.NewGetParametersByPathPaginator(client, &input),
	}

	for i := 0; i < v.NumField(); i++ {
		tag := v.Type().Field(i).Tag.Get(tagName)
		if tag == "" {
			continue
		}
		tagParts := strings.Split(tag, ",")
		if len(tagParts) == 0 {
			continue
		}
		suffix := strings.Trim(tagParts[0], "/")
		name := path + "/" + suffix
		optional := len(tagParts) > 1 && tagParts[1] == "optional"

		f := v.Field(i)
		if !f.CanSet() {
			panic(fmt.Errorf("invalid field with ssm tag (can't set): %+v", f))
		}
		if f.Kind() != reflect.String {
			panic(fmt.Errorf("invalid field with ssm tag (not a string): %+v", f))
		}

		r.setters[name] = append(r.setters[name], f.SetString)
		if !optional {
			r.missing[name] = struct{}{}
		}
	}

	return &r
}

const tagName = "ssm"

type request struct {
	lock      sync.Mutex
	done      bool
	missing   map[string]struct{}
	setters   map[string][]func(string)
	paginator *ssm.GetParametersByPathPaginator
}

func (r *request) Send(ctx context.Context) error {
	r.lock.Lock()
	defer r.lock.Unlock()
	if !r.done {
		r.done = true
	} else {
		panic("request executed more than once")
	}

	for r.paginator.HasMorePages() {
		page, err := r.paginator.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, parameter := range page.Parameters {
			for _, setter := range r.setters[*parameter.Name] {
				setter(*parameter.Value)
			}
			delete(r.missing, *parameter.Name)
		}
	}

	if len(r.missing) > 0 {
		missingParameters := make(MissingParameters, 0, len(r.missing))
		for name := range r.missing {
			missingParameters = append(missingParameters, name)
		}
		sort.Strings(missingParameters)
		return missingParameters
	}

	return nil
}
