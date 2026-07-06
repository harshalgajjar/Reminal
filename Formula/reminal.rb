class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "1.7.0"
  license "AGPL-3.0-or-later"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.7.0/reminal_1.7.0_darwin_arm64.tar.gz"
      sha256 "0bc4c7c2e42f99b832ebef746ed2e89e7268a0855c5142e0d3945f55749162f4"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.7.0/reminal_1.7.0_darwin_amd64.tar.gz"
      sha256 "280f8f36ae4a28ce41707062f96ce1e15fc83e98a2d1d1c9b1f5fc31a57c6234"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.7.0/reminal_1.7.0_linux_arm64.tar.gz"
      sha256 "97108335ba1a92938df74bad46af00cb65aa2954dbd6c85f836a385b450e67cd"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.7.0/reminal_1.7.0_linux_amd64.tar.gz"
      sha256 "ac5966e195a2269465a198d164c3aac191cc19c53990c4dc50e01ce31ec1da9a"
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
