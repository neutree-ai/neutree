package scheme

import (
	"testing"

	"github.com/stretchr/testify/assert"

	schemetesting "github.com/neutree-ai/neutree/pkg/scheme/testing"
)

func TestCodecFactory_Decode(t *testing.T) {
	tests := []struct {
		name        string
		inputJSON   string
		setupScheme func(*Scheme)
		expectError bool
	}{
		{
			name: "Decode success",
			inputJSON: `{
				"kind": "TestObject"
			}`,
			setupScheme: func(s *Scheme) {
				s.AddKnownTypes(&schemetesting.TestObject{})
			},
			expectError: false,
		},
		{
			name: "Decode object failed - unregistered type",
			inputJSON: `{
				"kind": "TestObject",
			}`,
			setupScheme: func(*Scheme) {},
			expectError: true,
		},
		{
			name: "Decode failed - not json",
			inputJSON: `
				"api_version": "v1",
				"kind": "Role",`,
			setupScheme: func(*Scheme) {},
			expectError: true,
		},
		{
			name: "Decode failed - missing kind",
			inputJSON: `{
				"api_version": "v1",
				"metadata": {
					"name": "test-role",
					"workspace": "default"
				},
				"spec": {
					"preset_key": "admin",
					"permissions": ["read", "write"]
				},
				"status": {
					"phase": "Created"
				}
			}`,
			setupScheme: func(*Scheme) {},
			expectError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewScheme()
			tt.setupScheme(s)
			decoder := NewCodecFactory(s).Decoder()
			obj, err := decoder.Decode([]byte(tt.inputJSON), "")
			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, obj)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, obj)
			}
		})
	}
}

func Test_Scheme_AddKnownTypes(t *testing.T) {
	s := NewScheme()
	s.AddKnownTypes(&schemetesting.TestObject{})

	_, _, err := s.gvkToType("TestObject") // should not return error
	assert.NoError(t, err)

	_, _, err = s.gvkToType("UnknownType") // should return error
	assert.Error(t, err)
}

func Test_Scheme_AddKnownTableTypes(t *testing.T) {
	s := NewScheme()
	s.AddKnownTypes(&schemetesting.TestObject{})
	s.AddKnownTableTypes(map[string]string{
		"testobjects": "TestObject",
	})

	table, exist := s.KindToTable("TestObject")
	assert.True(t, exist)
	assert.Equal(t, "testobjects", table)

	table, exist = s.KindToTable("UnknownType")
	assert.False(t, exist)
	assert.Equal(t, "", table)
}

func Test_Scheme_ResolveKind(t *testing.T) {
	s := NewScheme()
	s.AddKnownTypes(&schemetesting.TestObject{})
	s.AddKnownTableTypes(map[string]string{
		"test_objects": "TestObject",
	})

	tests := []struct {
		name     string
		input    string
		wantKind string
		wantOK   bool
	}{
		{
			name:     "exact kind match",
			input:    "TestObject",
			wantKind: "TestObject",
			wantOK:   true,
		},
		{
			name:     "exact table match",
			input:    "test_objects",
			wantKind: "TestObject",
			wantOK:   true,
		},
		{
			name:     "case-insensitive kind match",
			input:    "testobject",
			wantKind: "TestObject",
			wantOK:   true,
		},
		{
			name:     "case-insensitive kind match uppercase",
			input:    "TESTOBJECT",
			wantKind: "TestObject",
			wantOK:   true,
		},
		{
			name:     "case-insensitive table match",
			input:    "Test_Objects",
			wantKind: "TestObject",
			wantOK:   true,
		},
		{
			name:   "unknown kind",
			input:  "DoesNotExist",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, ok := s.ResolveKind(tt.input)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.wantKind, kind)
			}
		})
	}
}

func Test_Scheme_New(t *testing.T) {
	s := NewScheme()
	s.AddKnownTypes(&schemetesting.TestObject{})

	obj, err := s.New("TestObject")
	assert.NoError(t, err)
	assert.IsType(t, &schemetesting.TestObject{}, obj)

	// Test creating an unregistered type
	obj, err = s.New("UnknownType")
	assert.Error(t, err)
	assert.Nil(t, obj)
}
