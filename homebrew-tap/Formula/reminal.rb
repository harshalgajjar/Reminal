class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "1.2.6"
  license "AGPL-3.0-or-later"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.2.6/reminal_1.2.6_darwin_arm64.tar.gz"
      sha256 "621cc696db8d22717f5b3913c5a056973801708ff60b00a1c8ca5886940fb8e5"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.2.6/reminal_1.2.6_darwin_amd64.tar.gz"
      sha256 "63a5834fb4620812549e95e77c9b1fcfe4ec3a8f0ab8b410a2bd4c9ce152bc05"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.2.6/reminal_1.2.6_linux_arm64.tar.gz"
      sha256 "a88bd95610934b2c3c1384d5adbd082c945c2cdef54423dfd2fcef94e5c62b77"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.2.6/reminal_1.2.6_linux_amd64.tar.gz"
      sha256 "40eb9e56b2a607eab2aec8b414c67871e799ec6e45dd5e91c417c9230ad8e03a"
    end
  end

  depends_on "go" => :build if build.head?

  def install
    if build.head?
      system "go", "build", "-ldflags=#{ldflags}", "-o", bin/"reminal", "./cmd/reminal"
    else
      bin.install "reminal"
    end
  end

  def ldflags
    "-s -w " \
      "-X main.version=#{version} " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.futuristic.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.futuristic.workers.dev"
  end

  def caveats
    <<~EOS
      reminal connects to the hosted relay automatically — no setup needed.

        reminal              # share your terminal
        reminal --connect ID --pin PIN
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/reminal version")
  end
end
