class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "1.8.3"
  license "AGPL-3.0-or-later"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.8.3/reminal_1.8.3_darwin_arm64.tar.gz"
      sha256 "787ad0f65294e00b54fe7b47524eff2df7eb17c77a5750972dc646fd0af5389d"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.8.3/reminal_1.8.3_darwin_amd64.tar.gz"
      sha256 "1d3eec7281b6cb554ef53aad08c484fc52bc128bece97866ddcd595b7413edbb"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.8.3/reminal_1.8.3_linux_arm64.tar.gz"
      sha256 "534287ea55856fda592a77d1076c29572512c756aaa7ae2534a1681d84ae494b"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.8.3/reminal_1.8.3_linux_amd64.tar.gz"
      sha256 "cbd5b55642adc784a6cc411f921becab743171a97e15ed902f6486651c4346d7"
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
