class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.7.12"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.12/reminal_0.7.12_darwin_arm64.tar.gz"
      sha256 "182e9ddaf720dadf3b82f71062aea89f2cc57984ec95ab5e19fb03e8a853967c"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.12/reminal_0.7.12_darwin_amd64.tar.gz"
      sha256 "d6cd054fbda69f30594eec5eb0b22c053b32dd7647409f1cde51cba825281123"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.12/reminal_0.7.12_linux_arm64.tar.gz"
      sha256 "66716b9a871df45a1f07ad0274bd2acf840b1ede87ecfb7f41c96281797aa21a"
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
