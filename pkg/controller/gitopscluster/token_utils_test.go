package gitopscluster

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetManagedClusterToken(t *testing.T) {
	testCases := []struct {
		name          string
		inputData     []byte
		expectedToken string
		expectError   bool
	}{
		{
			name: "Valid JSON with bearer token",
			inputData: []byte(`{
				"bearerToken": "test-token-123",
				"tlsClientConfig": {
					"insecure": true
				}
			}`),
			expectedToken: "test-token-123",
			expectError:   false,
		},
		{
			name:        "Nil input data",
			inputData:   nil,
			expectError: true,
		},
		{
			name:        "Empty input data",
			inputData:   []byte{},
			expectError: true,
		},
		{
			name:        "Invalid JSON",
			inputData:   []byte(`{"bearerToken": "token", "invalid": json}`),
			expectError: true,
		},
		{
			name: "Valid JSON but empty bearer token",
			inputData: []byte(`{
				"bearerToken": "",
				"tlsClientConfig": {
					"insecure": false
				}
			}`),
			expectedToken: "",
			expectError:   false,
		},
		{
			name: "Valid JSON with missing bearer token field",
			inputData: []byte(`{
				"tlsClientConfig": {
					"insecure": true
				}
			}`),
			expectedToken: "",
			expectError:   false,
		},
		{
			name: "Malformed JSON with extra characters",
			inputData: []byte(`{
				"bearerToken": "test-token"
			}extra-characters`),
			expectError: true,
		},
		{
			name: "JSON with additional fields",
			inputData: []byte(`{
				"bearerToken": "test-token-456",
				"tlsClientConfig": {
					"insecure": false,
					"certData": "cert-data"
				},
				"server": "https://example.com",
				"additionalField": "value"
			}`),
			expectedToken: "test-token-456",
			expectError:   false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			token, err := getManagedClusterToken(tc.inputData)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expectedToken, token)
			}
		})
	}
}

func TestGetConfigMapDuck(t *testing.T) {
	testCases := []struct {
		name           string
		configMapName  string
		namespace      string
		apiVersion     string
		kind           string
		expectedResult map[string]string
	}{
		{
			name:          "PlacementRule config map",
			configMapName: "acm-placementrule",
			namespace:     "test-namespace",
			apiVersion:    "apps.open-cluster-management.io/v1",
			kind:          "placementrules",
			expectedResult: map[string]string{
				"apiVersion":    "apps.open-cluster-management.io/v1",
				"kind":          "placementrules",
				"statusListKey": "decisions",
				"matchKey":      "clusterName",
			},
		},
		{
			name:          "Placement config map",
			configMapName: "acm-placement",
			namespace:     "test-namespace-2",
			apiVersion:    "cluster.open-cluster-management.io/v1beta1",
			kind:          "placementdecisions",
			expectedResult: map[string]string{
				"apiVersion":    "cluster.open-cluster-management.io/v1beta1",
				"kind":          "placementdecisions",
				"statusListKey": "decisions",
				"matchKey":      "clusterName",
			},
		},
		{
			name:          "Empty values",
			configMapName: "",
			namespace:     "",
			apiVersion:    "",
			kind:          "",
			expectedResult: map[string]string{
				"apiVersion":    "",
				"kind":          "",
				"statusListKey": "decisions",
				"matchKey":      "clusterName",
			},
		},
		{
			name:          "Custom values with special characters",
			configMapName: "test-config-map-with-dashes",
			namespace:     "test-namespace-with-underscores_123",
			apiVersion:    "custom.api.group/v1alpha1",
			kind:          "CustomKind",
			expectedResult: map[string]string{
				"apiVersion":    "custom.api.group/v1alpha1",
				"kind":          "CustomKind",
				"statusListKey": "decisions",
				"matchKey":      "clusterName",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := getConfigMapDuck(tc.configMapName, tc.namespace, tc.apiVersion, tc.kind)

			// Verify metadata
			assert.Equal(t, tc.configMapName, result.ObjectMeta.Name)
			assert.Equal(t, tc.namespace, result.ObjectMeta.Namespace)

			// Verify data map
			assert.Equal(t, tc.expectedResult, result.Data)

			// Verify that the data map always has the fixed fields
			assert.Equal(t, "decisions", result.Data["statusListKey"])
			assert.Equal(t, "clusterName", result.Data["matchKey"])
		})
	}
}
