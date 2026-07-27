package pod

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// TestFrontedPort is where the port decision lives, and the one place a silent
// wrong answer is most available: every fixture anyone would naturally reach
// for is a web server on 80, so "always 80" passes a careless suite.
func TestFrontedPort(t *testing.T) {
	tests := []struct {
		name        string
		ports       []corev1.ContainerPort
		want        int32
		wantAssumed bool
	}{
		{
			// `kubectl run nginx --image=nginx` declares nothing at all.
			name:        "no declared port is assumed to be 80",
			want:        80,
			wantAssumed: true,
		},
		{
			// The case that catches "always 80".
			name:  "a declared port wins over the default",
			ports: []corev1.ContainerPort{{ContainerPort: 8080}},
			want:  8080,
		},
		{
			name:  "a declared 3000 is not 80 either",
			ports: []corev1.ContainerPort{{ContainerPort: 3000}},
			want:  3000,
		},
		{
			name: "http is preferred out of several",
			ports: []corev1.ContainerPort{
				{Name: "metrics", ContainerPort: 9090},
				{Name: "http", ContainerPort: 8080},
			},
			want: 8080,
		},
		{
			name: "https is the second choice",
			ports: []corev1.ContainerPort{
				{Name: "metrics", ContainerPort: 9090},
				{Name: "https", ContainerPort: 8443},
			},
			want: 8443,
		},
		{
			// `kubectl run --port` produces an unnamed port, so a Pod built
			// that way can never be disambiguated by name. Refusing would be a
			// dead end: container ports cannot be edited on a running Pod.
			name: "several unnamed ports take the first, and say so",
			ports: []corev1.ContainerPort{
				{ContainerPort: 8080},
				{ContainerPort: 9090},
			},
			want:        8080,
			wantAssumed: true,
		},
		{
			name: "a UDP port is not a candidate",
			ports: []corev1.ContainerPort{
				{ContainerPort: 53, Protocol: corev1.ProtocolUDP},
				{ContainerPort: 8080, Protocol: corev1.ProtocolTCP},
			},
			want: 8080,
		},
		{
			name: "only UDP ports leaves nothing declared, so the default applies",
			ports: []corev1.ContainerPort{
				{ContainerPort: 53, Protocol: corev1.ProtocolUDP},
			},
			want:        80,
			wantAssumed: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pod := &corev1.Pod{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Ports: tc.ports}},
			}}
			got, assumed := frontedPort(pod)
			if got != tc.want {
				t.Errorf("frontedPort() = %d, want %d", got, tc.want)
			}
			if assumed != tc.wantAssumed {
				t.Errorf("assumed = %v, want %v — the guess has to be visible or nobody can debug it",
					assumed, tc.wantAssumed)
			}
		})
	}
}

// TestFrontedPortAcrossContainers: a sidecar declaring a port must not hide the
// app's, and the first declared TCP port across all containers is the fallback.
func TestFrontedPortAcrossContainers(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{
		{Name: "app", Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 3000}}},
		{Name: "sidecar", Ports: []corev1.ContainerPort{{Name: "metrics", ContainerPort: 9090}}},
	}}}

	got, assumed := frontedPort(pod)
	if got != 3000 {
		t.Errorf("frontedPort() = %d, want the container port named http", got)
	}
	if assumed {
		t.Error("assumed = true, but http was declared")
	}
}
