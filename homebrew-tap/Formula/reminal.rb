class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.3.4"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.4/reminal_0.3.4_darwin_arm64.tar.gz"
      sha256 "2cdeb6775e57639b0f0c20d4190720ac1f65b0731297f660de8e258c0b2f432d"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.4/reminal_0.3.4_darwin_amd64.tar.gz"
      sha256 "2564c6fe9d23f12ee0c7a6b1f4f675253f73f670a2aff1029b6c54ed6e1fc43d"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.4/reminal_0.3.4_linux_arm64.tar.gz"
      sha256 "5dbaa360b7b718a5294ed06b5fecc62655b9fbc8b93883221d98aad96dee7853"
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
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.reminal.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.reminal.workers.dev"
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
