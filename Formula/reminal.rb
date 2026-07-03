class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "1.1.1"
  license "AGPL-3.0-or-later"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.1.1/reminal_1.1.1_darwin_arm64.tar.gz"
      sha256 "93285201a9226fb39811b00b2338cbd10fa040f51acade5dca2d8d648c5f25f9"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.1.1/reminal_1.1.1_darwin_amd64.tar.gz"
      sha256 "9582558443b51461a3609377cc37d95c0adcfe71445155e642a98cd62b492964"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.1.1/reminal_1.1.1_linux_arm64.tar.gz"
      sha256 "191ce12266cd2d6a5e25cb45c75b31cf5e0b7b86e9ef4e3da140aa40335d7b42"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.1.1/reminal_1.1.1_linux_amd64.tar.gz"
      sha256 "138e00cc8aa5f144c72ec3e99d459ad5e95694aa1b2dd2db1ae542833cbab655"
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
