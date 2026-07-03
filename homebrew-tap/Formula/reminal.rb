class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "1.2.4"
  license "AGPL-3.0-or-later"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.2.4/reminal_1.2.4_darwin_arm64.tar.gz"
      sha256 "d78fd3f16cc1d35647cb8d61ea713318a915d5d857d59b446c73cf0da2d0a775"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.2.4/reminal_1.2.4_darwin_amd64.tar.gz"
      sha256 "df8cf0a3bb912a2c840d28e4f135c5823393e7b7e2c53fa3c483decb29b2b080"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.2.4/reminal_1.2.4_linux_arm64.tar.gz"
      sha256 "b9db61e4ea737fa98cd4683b8f80fec589667bd8f41d4865d5214a2cb8276764"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.2.4/reminal_1.2.4_linux_amd64.tar.gz"
      sha256 "49f45e4387f329e79a387626db5f733d3448e1a28d5379bc3758948f4c5fdfe3"
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
