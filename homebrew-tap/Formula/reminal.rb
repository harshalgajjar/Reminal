class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "1.1.0"
  license "AGPL-3.0-or-later"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.1.0/reminal_1.1.0_darwin_arm64.tar.gz"
      sha256 "92f35abc4a39581498e87ee9e5a165d20148d7ef8a426aa35a97b5b2ad38909b"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.1.0/reminal_1.1.0_darwin_amd64.tar.gz"
      sha256 "9b596c1438d1dc9be5ba52945de11d50fbabbdc138385307703d3924ce1c6230"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.1.0/reminal_1.1.0_linux_arm64.tar.gz"
      sha256 "f4123f4c58353865d3e03da5c466cda60f3d9d3fa9ea93b0f8fc2d8dbbf714d9"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.1.0/reminal_1.1.0_linux_amd64.tar.gz"
      sha256 "5be5a17c299efcddfc80eab32e2e3165cb3a3ebc0739db4fee6de96492495fd8"
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
