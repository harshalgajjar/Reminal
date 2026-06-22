class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.3.5"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.5/reminal_0.3.5_darwin_arm64.tar.gz"
      sha256 "0be33344bd7c374a2d6d468a22c3bc085e836fddc27302e11f909553a4aa8c0a"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.5/reminal_0.3.5_darwin_amd64.tar.gz"
      sha256 "62a1f6a5d4caa2e5048abe3ab3ad120f23e0052d00342b73d09e440c509af0d9"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.5/reminal_0.3.5_linux_arm64.tar.gz"
      sha256 "6f2a571bcc6a476271b3c9c4a74832736f57cb251fb3e9b4df2fd99886e92bbe"
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
