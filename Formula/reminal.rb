class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "1.8.1"
  license "AGPL-3.0-or-later"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.8.1/reminal_1.8.1_darwin_arm64.tar.gz"
      sha256 "6ee94a0cc4652d07a7928a4b05bae2c3f471e0986425b4a126bd2a0a2dc6e0fd"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.8.1/reminal_1.8.1_darwin_amd64.tar.gz"
      sha256 "8cdc6ea764fccd32e92bc5e54a7d95a984c8fcfe6f89593cd0918ed4f554cd15"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.8.1/reminal_1.8.1_linux_arm64.tar.gz"
      sha256 "005d5cf4dd856a16df92afacd9ea16ee84aee757d06a716e3c6d2a01895f7f2a"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.8.1/reminal_1.8.1_linux_amd64.tar.gz"
      sha256 "de447cbd6a595ffdfacacf9c8296b474bb43e23f27b12f499a352aa97e4efcbe"
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
