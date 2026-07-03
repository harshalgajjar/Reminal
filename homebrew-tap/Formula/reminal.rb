class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "1.3.1"
  license "AGPL-3.0-or-later"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.3.1/reminal_1.3.1_darwin_arm64.tar.gz"
      sha256 "51447aed8d8408b0c9b3f28047a3364a626230a6766d6abcf45cbde467b201e6"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.3.1/reminal_1.3.1_darwin_amd64.tar.gz"
      sha256 "60b79c6da6efbe5ef189b18d3267f136fd89f5703aeec73eedbb1c3d54936556"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.3.1/reminal_1.3.1_linux_arm64.tar.gz"
      sha256 "0fe12c57a908072dc6b1b6132dee4f96a4465a285a5043a78964ef248732668a"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.3.1/reminal_1.3.1_linux_amd64.tar.gz"
      sha256 "9a8789f2767bb42328f4bbb1c10c5832b4430c10efa48159d66cd77511f32bc3"
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
