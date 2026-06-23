class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.7.2"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.2/reminal_0.7.2_darwin_arm64.tar.gz"
      sha256 "cc670c03a635ce6740f744fd8e22a7d4b1adef4aa8c4a3a2601cf1234a7312cd"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.2/reminal_0.7.2_darwin_amd64.tar.gz"
      sha256 "4ffc65b1f3ebf21359ac36bbfbfcc683c087d9c30a1c7d4796a456f8065b8f1c"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.2/reminal_0.7.2_linux_arm64.tar.gz"
      sha256 "67b520c66b369ff5a112f3673021c4717e380ec9b7defc366948487475dbb38b"
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
