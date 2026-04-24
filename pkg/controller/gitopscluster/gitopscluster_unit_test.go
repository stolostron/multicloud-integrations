// Copyright 2021 The Kubernetes Authors.
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

package gitopscluster

import (
	"strings"
	"testing"

	"github.com/onsi/gomega"
)

func TestTruncateLabelValue(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "short value unchanged",
			input:    "example.com",
			expected: "example.com",
		},
		{
			name:     "exactly 63 chars ending with alphanumeric",
			input:    "api.observability.stage.mastercard-uscentral-tst-az1.mks.cloud1",
			expected: "api.observability.stage.mastercard-uscentral-tst-az1.mks.cloud1",
		},
		{
			// Regression test: hostname longer than 63 chars whose 63-char prefix ends with '.'
			// e.g. api.observability.stage.mastercard-uscentral-tst-az1.mks.cloud.zlocal
			// truncated to 63 chars becomes "api.observability.stage.mastercard-uscentral-tst-az1.mks.cloud."
			name:     "truncation produces trailing dot",
			input:    "api.observability.stage.mastercard-uscentral-tst-az1.mks.cloud.zlocal",
			expected: "api.observability.stage.mastercard-uscentral-tst-az1.mks.cloud",
		},
		{
			name:     "truncation produces trailing dash",
			input:    strings.Repeat("a", 62) + "-extra",
			expected: strings.Repeat("a", 62),
		},
		{
			name:     "truncation produces trailing underscore",
			input:    strings.Repeat("a", 62) + "_extra",
			expected: strings.Repeat("a", 62),
		},
		{
			name:     "truncation produces trailing other non-alphanumeric character",
			input:    strings.Repeat("a", 62) + "@extra",
			expected: strings.Repeat("a", 62),
		},
		{
			name:     "multiple trailing separators trimmed after truncation",
			input:    strings.Repeat("a", 60) + "...extra",
			expected: strings.Repeat("a", 60),
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "result never exceeds 63 chars",
			input:    strings.Repeat("b", 70),
			expected: strings.Repeat("b", 63),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateLabelValue(tt.input)
			g.Expect(result).To(gomega.Equal(tt.expected), "input: %q", tt.input)
		})
	}
}
