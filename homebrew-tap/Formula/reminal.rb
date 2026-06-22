class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.5.4"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.5.4/reminal_0.5.4_darwin_arm64.tar.gz"
      sha256 "1de3e9b71055297f2345fbda3d628b157f7ca6947298476910c0226381e0b577"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.5.4/reminal_0.5.4_darwin_amd64.tar.gz"
      sha256 "6efde91a76b4c890447f3c9977092dba34d28a28c29e1807c91822ffcbe4aa4d"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.5.4/reminal_0.5.4_linux_arm64.tar.gz"
      sha256 "a005da29090d7115a33180a8673e17e76c03adb10651ddaa8c93c5c332bb56c5"
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
