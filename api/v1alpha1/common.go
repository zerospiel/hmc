/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

// Providers is a structure holding different types of CAPI providers
type Providers struct {
	// InfrastructureProviders is the list of CAPI infrastructure providers
	InfrastructureProviders []string `json:"infrastructure,omitempty"`
	// BootstrapProviders is the list of CAPI bootstrap providers
	BootstrapProviders []string `json:"bootstrap,omitempty"`
	// ControlPlaneProviders is the list of CAPI control plane providers
	ControlPlaneProviders []string `json:"controlPlane,omitempty"`
}